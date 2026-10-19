//! 单连接原生 WebSocket RPC，账号作用域与连接池由宿主持有

use futures::{SinkExt, Stream, StreamExt};
use gateway_core::account::OutboundProxy;
use provider_openai::transport::websocket::{CodexWebSocketConnection, connect_prepared_websocket};
use sha2::{Digest, Sha256};
use std::{
    collections::HashSet,
    pin::Pin,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicI32, Ordering},
    },
    time::Duration,
};
use tokio::sync::{mpsc, oneshot, watch};
use tokio_tungstenite::tungstenite::{
    self, Message,
    protocol::frame::{CloseFrame, coding::CloseCode},
};
use tokio_util::sync::CancellationToken;
use tonic::{Request, Response, Status, Streaming, transport::Endpoint};

use crate::{Runner, wire};
use wire::{
    Header, SendState, WebSocketCommand, WebSocketEvent, WebSocketMessage, WebSocketMessageKind,
    WebSocketStart, web_socket_command, web_socket_event,
};

pub const MAX_MESSAGE_BYTES: usize = 16 * 1024 * 1024;
pub type Events = Pin<Box<dyn Stream<Item = Result<WebSocketEvent, Status>> + Send>>;

#[derive(Clone)]
struct Output {
    control: mpsc::Sender<WebSocketEvent>,
    data: mpsc::Sender<WebSocketEvent>,
}
#[derive(Clone)]
struct State {
    cancel: CancellationToken,
    connected: Arc<AtomicBool>,
    send_state: Arc<AtomicI32>,
    close: Arc<Mutex<wire::WebSocketClose>>,
    input_error: Arc<Mutex<Option<&'static str>>>,
}
impl State {
    fn watermark(&self) -> i32 {
        self.send_state.load(Ordering::Acquire)
    }
    fn close(&self, code: u32, reason: &str) {
        *self
            .close
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner) = wire::WebSocketClose {
            code,
            reason: reason.into(),
        };
        self.cancel.cancel();
    }
    fn input_failed(&self, code: &'static str) {
        *self
            .input_error
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner) = Some(code);
        self.close(1002, "invalid host command");
    }
}
fn event(state: &State, frame: web_socket_event::Frame) -> WebSocketEvent {
    WebSocketEvent {
        send_state: state.watermark(),
        frame: Some(frame),
    }
}
fn failure(state: &State, code: &str, message: &str) -> WebSocketEvent {
    event(
        state,
        web_socket_event::Frame::Failure(wire::AttemptFailure {
            code: code.into(),
            message: message.into(),
            ..Default::default()
        }),
    )
}
async fn emit(tx: &mpsc::Sender<WebSocketEvent>, state: &State, value: WebSocketEvent) -> bool {
    tokio::select! { biased; _ = state.cancel.cancelled() => false, result = tx.send(value) => result.is_ok() }
}
async fn fail(output: &Output, state: &State, code: &str, message: &str) {
    let _ = emit(&output.control, state, failure(state, code, message)).await;
}

// 与生成的 tonic 服务签名一致，避免为缩小错误枚举而改写 RPC 合同
#[allow(clippy::result_large_err)]
pub async fn open(
    runner: Runner,
    request: Request<Streaming<WebSocketCommand>>,
) -> Result<Response<Events>, Status> {
    if request
        .metadata()
        .get("authorization")
        .and_then(|v| v.to_str().ok())
        != Some(runner.token.as_str())
    {
        return Err(Status::unauthenticated("driver authentication required"));
    }
    let mut commands = request.into_inner();
    let state = State {
        cancel: runner.shutdown.child_token(),
        connected: Arc::new(AtomicBool::new(false)),
        send_state: Arc::new(AtomicI32::new(SendState::NotSent as i32)),
        close: Arc::new(Mutex::new(wire::WebSocketClose {
            code: 1000,
            reason: "host cancelled".into(),
        })),
        input_error: Arc::new(Mutex::new(None)),
    };
    let start = match commands.message().await?.and_then(|v| v.frame) {
        Some(web_socket_command::Frame::Start(v)) => *v,
        _ => {
            return Ok(Response::new(Box::pin(tokio_stream::once(Ok(failure(
                &state,
                "invalid_start",
                "first frame must bind one WebSocket session",
            ))))));
        }
    };
    let (control_tx, mut control_rx) = mpsc::channel(16);
    let (data_tx, mut data_rx) = mpsc::channel(2);
    let output = Output {
        control: control_tx,
        data: data_tx,
    };
    let (write_tx, write_rx) = mpsc::channel(1);
    let (permit_tx, permit_rx) = oneshot::channel();
    let input_state = state.clone();
    tokio::spawn(async move {
        let input = tokio::spawn(read_commands(commands, input_state, permit_tx, write_tx));
        run_session(runner, start, permit_rx, write_rx, state.clone(), output).await;
        state.cancel.cancel();
        input.abort();
        let _ = input.await;
    });
    let stream = async_stream::stream! {
        let mut control_open = true;
        let mut data_open = true;
        while control_open || data_open {
            tokio::select! {
                biased;
                value = control_rx.recv(), if control_open => match value { Some(value) => yield Ok(value), None => control_open = false },
                value = data_rx.recv(), if data_open => match value { Some(value) => yield Ok(value), None => data_open = false },
            }
        }
    };
    Ok(Response::new(Box::pin(stream)))
}

async fn read_commands(
    mut commands: Streaming<WebSocketCommand>,
    state: State,
    permit: oneshot::Sender<()>,
    writes: mpsc::Sender<WebSocketMessage>,
) {
    let mut permit = Some(permit);
    let mut expected_sequence = 1u64;
    loop {
        let value = tokio::select! { biased; _ = state.cancel.cancelled() => return, value = commands.message() => value };
        match value {
            Ok(Some(WebSocketCommand {
                frame: Some(web_socket_command::Frame::PermitConnect(true)),
            })) if permit.is_some() => {
                let _ = permit.take().expect("checked permit").send(());
            }
            Ok(Some(WebSocketCommand {
                frame: Some(web_socket_command::Frame::Message(message)),
            })) => {
                if message.data.len() > MAX_MESSAGE_BYTES {
                    *state
                        .input_error
                        .lock()
                        .unwrap_or_else(std::sync::PoisonError::into_inner) =
                        Some("message_too_large");
                    state.close(1009, "message too large");
                    return;
                }
                if !state.connected.load(Ordering::Acquire)
                    || message.sequence != expected_sequence
                    || validate_message(&message).is_err()
                {
                    state.input_failed("invalid_message");
                    return;
                }
                expected_sequence = match expected_sequence.checked_add(1) {
                    Some(v) => v,
                    None => {
                        state.input_failed("sequence_exhausted");
                        return;
                    }
                };
                if matches!(
                    message.kind(),
                    WebSocketMessageKind::Text | WebSocketMessageKind::Binary
                ) {
                    let _ = state.send_state.compare_exchange(
                        SendState::NotSent as i32,
                        SendState::Ambiguous as i32,
                        Ordering::AcqRel,
                        Ordering::Acquire,
                    );
                }
                // 满业务队列不能阻塞后续控制关闭，过量并行写入明确结束此连接
                if writes.try_send(message).is_err() {
                    state.input_failed("write_backpressure");
                    return;
                }
            }
            Ok(Some(WebSocketCommand {
                frame: Some(web_socket_command::Frame::Close(close)),
            })) => {
                let code = if close.code == 0 { 1000 } else { close.code };
                if code > u16::MAX as u32
                    || !CloseCode::from(code as u16).is_allowed()
                    || close.reason.len() > 123
                {
                    state.input_failed("invalid_close");
                } else {
                    state.close(code, &close.reason);
                }
                return;
            }
            Ok(Some(WebSocketCommand {
                frame: Some(web_socket_command::Frame::Cancel(true)),
            }))
            | Err(_)
            | Ok(None) => {
                state.close(1000, "host cancelled");
                return;
            }
            _ => {
                state.input_failed("invalid_command");
                return;
            }
        }
    }
}

fn validate_message(message: &WebSocketMessage) -> Result<(), ()> {
    let kind = WebSocketMessageKind::try_from(message.kind).map_err(|_| ())?;
    match kind {
        WebSocketMessageKind::Text
            if message.data.len() <= MAX_MESSAGE_BYTES
                && std::str::from_utf8(&message.data).is_ok() =>
        {
            Ok(())
        }
        WebSocketMessageKind::Binary if message.data.len() <= MAX_MESSAGE_BYTES => Ok(()),
        WebSocketMessageKind::Ping | WebSocketMessageKind::Pong if message.data.len() <= 125 => {
            Ok(())
        }
        WebSocketMessageKind::Close
            if message.data.len() <= 125
                && (message.data.is_empty()
                    || message.data.len() >= 2
                        && std::str::from_utf8(&message.data[2..]).is_ok()) =>
        {
            Ok(())
        }
        _ => Err(()),
    }
}
fn hash_valid(value: &str) -> bool {
    value.len() == 64 && value.bytes().all(|v| v.is_ascii_hexdigit())
}
fn validate_start(start: &WebSocketStart) -> Result<String, &'static str> {
    if start.account_id <= 0
        || start.binding_revision <= 0
        || start.credential_revision != 0
        || start.request_id.is_empty()
        || start.session_id.len() < 32
        || start.session_id.len() > 128
        || !hash_valid(&start.key_scope_hash)
        || !hash_valid(&start.credential_fingerprint)
        || start.handshake_timeout_ms < 0
    {
        return Err("invalid_scope");
    }
    let url = reqwest::Url::parse(&start.url).map_err(|_| "unsupported_endpoint")?;
    let loopback = url
        .host_str()
        .and_then(|v| v.parse::<std::net::IpAddr>().ok())
        .is_some_and(|v| v.is_loopback());
    if !((url.scheme() == "wss" && url.host_str() == Some("chatgpt.com"))
        || (url.scheme() == "ws" && loopback))
        || !matches!(
            url.path(),
            "/codex/responses" | "/backend-api/codex/responses"
        )
        || !url.username().is_empty()
        || url.password().is_some()
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return Err("unsupported_endpoint");
    }
    Ok(start.url.trim_end_matches("/codex/responses").to_owned())
}
fn headers(
    start: &WebSocketStart,
    identity: &wire::IdentityResponse,
) -> Result<Vec<(String, String)>, &'static str> {
    let mut protected_names = HashSet::new();
    let mut result = Vec::new();
    let mut authorization = None;
    for header in &identity.protected_headers {
        let name = reqwest::header::HeaderName::from_bytes(header.name.as_bytes())
            .map_err(|_| "invalid_headers")?;
        if !matches!(name.as_str(), "authorization" | "chatgpt-account-id")
            || !protected_names.insert(name.as_str().to_owned())
        {
            return Err("invalid_headers");
        }
        reqwest::header::HeaderValue::from_bytes(&header.value).map_err(|_| "invalid_headers")?;
        if name == reqwest::header::AUTHORIZATION {
            authorization = Some(header.value.as_slice());
        }
        result.push((
            name.as_str().to_owned(),
            String::from_utf8(header.value.clone()).map_err(|_| "invalid_headers")?,
        ));
    }
    let authorization = authorization.ok_or("identity_rejected")?;
    let digest = Sha256::digest(authorization);
    let digest = digest
        .iter()
        .map(|v| format!("{v:02x}"))
        .collect::<String>();
    if digest != start.credential_fingerprint.to_ascii_lowercase() {
        return Err("credential_scope_mismatch");
    }
    for header in &start.headers {
        let name = reqwest::header::HeaderName::from_bytes(header.name.as_bytes())
            .map_err(|_| "invalid_headers")?;
        if protected_names.contains(name.as_str())
            || matches!(
                name.as_str(),
                "authorization"
                    | "chatgpt-account-id"
                    | "host"
                    | "connection"
                    | "upgrade"
                    | "sec-websocket-key"
                    | "sec-websocket-version"
                    | "sec-websocket-extensions"
                    | "proxy-authorization"
            )
        {
            continue;
        }
        reqwest::header::HeaderValue::from_bytes(&header.value).map_err(|_| "invalid_headers")?;
        result.push((
            name.as_str().to_owned(),
            String::from_utf8(header.value.clone()).map_err(|_| "invalid_headers")?,
        ));
    }
    Ok(result)
}

async fn run_session(
    runner: Runner,
    start: WebSocketStart,
    permit: oneshot::Receiver<()>,
    writes: mpsc::Receiver<WebSocketMessage>,
    state: State,
    output: Output,
) {
    let base = match validate_start(&start) {
        Ok(v) => v,
        Err(code) => {
            fail(
                &output,
                &state,
                code,
                "WebSocket session or endpoint is invalid",
            )
            .await;
            return;
        }
    };
    let endpoint = match Endpoint::from_shared(runner.callback).map(|v| {
        v.connect_timeout(Duration::from_secs(10))
            .timeout(Duration::from_secs(10))
    }) {
        Ok(v) => v,
        Err(_) => {
            fail(
                &output,
                &state,
                "callback_configuration",
                "identity callback is invalid",
            )
            .await;
            return;
        }
    };
    let connected = tokio::select! { biased; _ = state.cancel.cancelled() => return, value = endpoint.connect() => value };
    let channel = match connected {
        Ok(v) => v,
        Err(_) => {
            fail(
                &output,
                &state,
                "callback_unavailable",
                "identity callback is unavailable",
            )
            .await;
            return;
        }
    };
    let mut callback = wire::host_callbacks_client::HostCallbacksClient::new(channel);
    let mut request = Request::new(wire::IdentityRequest {
        request_id: start.request_id.clone(),
        attempt_id: start.session_id.clone(),
        account_id: start.account_id,
        credential_revision: 0,
        binding_revision: start.binding_revision,
        session_id: start.session_id.clone(),
        key_scope_hash: start.key_scope_hash.clone(),
        credential_fingerprint: start.credential_fingerprint.clone(),
    });
    request.metadata_mut().insert(
        "authorization",
        runner.token.parse().expect("validated process token"),
    );
    let identity = tokio::select! { biased; _ = state.cancel.cancelled() => return, result = callback.resolve_identity(request) => match result { Ok(v) if v.get_ref().authorized => v.into_inner(), _ => { fail(&output, &state, "identity_rejected", "host denied the WebSocket scope").await; return; } } };
    if identity.proxy_url != start.proxy_url {
        fail(
            &output,
            &state,
            "proxy_identity_mismatch",
            "proxy identity changed",
        )
        .await;
        return;
    }
    let headers = match headers(&start, &identity) {
        Ok(v) => v,
        Err(code) => {
            fail(
                &output,
                &state,
                code,
                "credential or prepared headers do not match the session scope",
            )
            .await;
            return;
        }
    };
    let proxy = if identity.proxy_url.is_empty() {
        None
    } else {
        match OutboundProxy::parse(&identity.proxy_url) {
            Ok(v) => Some(v),
            Err(_) => {
                fail(&output, &state, "invalid_proxy", "proxy is invalid").await;
                return;
            }
        }
    };
    let connection = CodexWebSocketConnection::responses(
        &base,
        &tungstenite::handshake::client::generate_key(),
        headers,
    )
    .with_outbound_proxy(proxy);
    if !emit(
        &output.control,
        &state,
        event(&state, web_socket_event::Frame::Ready(wire::Ready {})),
    )
    .await
    {
        return;
    }
    let allowed = tokio::select! { biased; _ = state.cancel.cancelled() => false, result = permit => result.is_ok() };
    if !allowed {
        return;
    }
    let connect = connect_prepared_websocket(&connection, MAX_MESSAGE_BYTES);
    let connect = async {
        if start.handshake_timeout_ms == 0 {
            connect.await.map_err(Some)
        } else {
            tokio::time::timeout(
                Duration::from_millis(start.handshake_timeout_ms as u64),
                connect,
            )
            .await
            .map_err(|_| None)?
            .map_err(Some)
        }
    };
    let result = tokio::select! { biased; _ = state.cancel.cancelled() => return, _ = output.control.closed() => return, result = connect => result };
    let (socket, response) = match result {
        Ok(v) => v,
        Err(Some(tungstenite::Error::Http(response))) => {
            let head = wire::ResponseHead {
                status_code: u32::from(response.status().as_u16()),
                headers: response
                    .headers()
                    .iter()
                    .map(|(name, value)| Header {
                        name: name.as_str().into(),
                        value: value.as_bytes().to_vec(),
                    })
                    .collect(),
                protocol: "HTTP/1.1".into(),
            };
            let _ = emit(
                &output.control,
                &state,
                event(&state, web_socket_event::Frame::Handshake(head)),
            )
            .await;
            let mut value = failure(
                &state,
                "handshake_rejected",
                "upstream rejected the WebSocket handshake",
            );
            if let Some(web_socket_event::Frame::Failure(v)) = value.frame.as_mut() {
                v.upstream_status = u32::from(response.status().as_u16());
                v.raw_body = response
                    .body()
                    .as_deref()
                    .unwrap_or_default()
                    .iter()
                    .copied()
                    .take(8192)
                    .collect();
            }
            let _ = emit(&output.control, &state, value).await;
            return;
        }
        Err(None) => {
            fail(
                &output,
                &state,
                "handshake_timeout",
                "WebSocket handshake timed out",
            )
            .await;
            return;
        }
        Err(Some(_)) => {
            fail(
                &output,
                &state,
                "handshake_transport",
                "WebSocket handshake transport failed",
            )
            .await;
            return;
        }
    };
    state.connected.store(true, Ordering::Release);
    let head = wire::ResponseHead {
        status_code: u32::from(response.status().as_u16()),
        headers: response
            .headers()
            .iter()
            .map(|(name, value)| Header {
                name: name.as_str().into(),
                value: value.as_bytes().to_vec(),
            })
            .collect(),
        protocol: "HTTP/1.1".into(),
    };
    if !emit(
        &output.control,
        &state,
        event(&state, web_socket_event::Frame::Handshake(head)),
    )
    .await
    {
        return;
    }
    relay(socket, writes, state, output).await;
}

async fn relay(
    socket: provider_openai::transport::websocket::RawWsStream,
    mut writes: mpsc::Receiver<WebSocketMessage>,
    state: State,
    output: Output,
) {
    let (mut sink, mut source) = socket.split();
    let (flush_tx, mut flush_rx) = watch::channel(0u64);
    let writer_state = state.clone();
    let writer_output = output.clone();
    let mut writer = tokio::spawn(async move {
        loop {
            let command = tokio::select! {
                biased;
                _ = writer_state.cancel.cancelled() => break,
                result = flush_rx.changed() => { if result.is_err() { break; } tokio::select! { _ = writer_state.cancel.cancelled() => break, _ = sink.flush() => {} }; continue; },
                message = writes.recv() => message,
            };
            let Some(message) = command else {
                break;
            };
            let kind = message.kind();
            let frame = match kind {
                WebSocketMessageKind::Text => Message::Text(
                    String::from_utf8(message.data)
                        .expect("validated text")
                        .into(),
                ),
                WebSocketMessageKind::Binary => Message::Binary(message.data.into()),
                WebSocketMessageKind::Ping => Message::Ping(message.data.into()),
                WebSocketMessageKind::Pong => Message::Pong(message.data.into()),
                WebSocketMessageKind::Close => Message::Close(if message.data.is_empty() {
                    None
                } else {
                    Some(CloseFrame {
                        code: CloseCode::from(u16::from_be_bytes([
                            message.data[0],
                            message.data[1],
                        ])),
                        reason: String::from_utf8(message.data[2..].to_vec())
                            .expect("validated close")
                            .into(),
                    })
                }),
                _ => unreachable!("validated message kind"),
            };
            let sent = tokio::select! { biased; _ = writer_state.cancel.cancelled() => break, result = sink.send(frame) => result };
            if sent.is_err() {
                let _ = writer_output.control.try_send(failure(
                    &writer_state,
                    "websocket_write",
                    "upstream WebSocket write failed",
                ));
                writer_state.close(1006, "write failed");
                break;
            }
            if matches!(
                kind,
                WebSocketMessageKind::Text | WebSocketMessageKind::Binary
            ) {
                writer_state
                    .send_state
                    .store(SendState::Sent as i32, Ordering::Release);
            }
            if !emit(
                &writer_output.control,
                &writer_state,
                event(
                    &writer_state,
                    web_socket_event::Frame::WriteAck(wire::WebSocketWriteAck {
                        sequence: message.sequence,
                    }),
                ),
            )
            .await
            {
                break;
            }
            if kind == WebSocketMessageKind::Close {
                writer_state.close(1000, "host closed");
                break;
            }
        }
        let close = writer_state
            .close
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .clone();
        let code = if matches!(close.code, 1005 | 1006 | 1015) {
            1000
        } else {
            close.code as u16
        };
        let _ = tokio::time::timeout(
            Duration::from_millis(100),
            sink.send(Message::Close(Some(CloseFrame {
                code: CloseCode::from(code),
                reason: close.reason.into(),
            }))),
        )
        .await;
    });
    let reader_state = state.clone();
    let reader_output = output.clone();
    let mut reader = tokio::spawn(async move {
        let mut sequence = 0u64;
        loop {
            let message = tokio::select! {
                biased;
                _ = reader_state.cancel.cancelled() => {
                    // 关闭期间丢弃已经取消的业务输出，继续读取对端 close ACK，避免未读 TCP 数据导致 RST
                    drain_close(&mut source).await;
                    return;
                },
                value = source.next() => value,
            };
            match message {
                Some(Ok(Message::Text(data))) => {
                    sequence += 1;
                    if !emit(
                        &reader_output.data,
                        &reader_state,
                        event(
                            &reader_state,
                            web_socket_event::Frame::Message(WebSocketMessage {
                                sequence,
                                kind: WebSocketMessageKind::Text as i32,
                                data: data.as_bytes().to_vec(),
                            }),
                        ),
                    )
                    .await
                    {
                        drain_close(&mut source).await;
                        return;
                    }
                }
                Some(Ok(Message::Binary(data))) => {
                    sequence += 1;
                    if !emit(
                        &reader_output.data,
                        &reader_state,
                        event(
                            &reader_state,
                            web_socket_event::Frame::Message(WebSocketMessage {
                                sequence,
                                kind: WebSocketMessageKind::Binary as i32,
                                data: data.to_vec(),
                            }),
                        ),
                    )
                    .await
                    {
                        drain_close(&mut source).await;
                        return;
                    }
                }
                Some(Ok(Message::Ping(data))) => {
                    flush_tx.send_modify(|v| *v = v.wrapping_add(1));
                    let _ = reader_output.control.try_send(event(
                        &reader_state,
                        web_socket_event::Frame::Message(WebSocketMessage {
                            sequence: 0,
                            kind: WebSocketMessageKind::Ping as i32,
                            data: data.to_vec(),
                        }),
                    ));
                }
                Some(Ok(Message::Pong(data))) => {
                    let _ = reader_output.control.try_send(event(
                        &reader_state,
                        web_socket_event::Frame::Message(WebSocketMessage {
                            sequence: 0,
                            kind: WebSocketMessageKind::Pong as i32,
                            data: data.to_vec(),
                        }),
                    ));
                }
                Some(Ok(Message::Close(close))) => {
                    flush_tx.send_modify(|v| *v = v.wrapping_add(1));
                    let (code, reason) = close
                        .map(|v| (u32::from(u16::from(v.code)), v.reason.to_string()))
                        .unwrap_or((1005, String::new()));
                    let _ = emit(
                        &reader_output.data,
                        &reader_state,
                        event(
                            &reader_state,
                            web_socket_event::Frame::Closed(wire::WebSocketClosed {
                                code,
                                reason,
                                clean: true,
                            }),
                        ),
                    )
                    .await;
                    return;
                }
                Some(Ok(Message::Frame(_))) => {}
                Some(Err(_)) | None => {
                    let _ = reader_output.control.try_send(failure(
                        &reader_state,
                        "websocket_read",
                        "upstream WebSocket ended without a complete close",
                    ));
                    return;
                }
            }
        }
    });
    let completed = tokio::select! {
        biased;
        _ = state.cancel.cancelled() => 0,
        _ = output.data.closed() => 0,
        _ = &mut writer => 1,
        _ = &mut reader => 2,
    };
    state.cancel.cancel();
    if completed != 1
        && tokio::time::timeout(Duration::from_millis(200), &mut writer)
            .await
            .is_err()
    {
        writer.abort();
        let _ = writer.await;
    }
    if completed != 2
        && tokio::time::timeout(Duration::from_millis(300), &mut reader)
            .await
            .is_err()
    {
        reader.abort();
        let _ = reader.await;
    }
    if let Some(code) = *state
        .input_error
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner)
    {
        let _ =
            output
                .control
                .try_send(failure(&state, code, "host WebSocket command was rejected"));
    }
}

async fn drain_close<S>(source: &mut S)
where
    S: Stream<Item = Result<Message, tungstenite::Error>> + Unpin,
{
    let _ = tokio::time::timeout(Duration::from_millis(200), async {
        while let Some(message) = source.next().await {
            if matches!(message, Ok(Message::Close(_)) | Err(_)) {
                break;
            }
        }
    })
    .await;
}
