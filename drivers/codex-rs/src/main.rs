//! 宿主准备请求的 CPR HTTP/SSE 驱动，认证与账号管理仍由 Sub2API 持有

use std::{
    collections::HashSet,
    env,
    error::Error as StdError,
    io::{Cursor, Write},
    pin::Pin,
    time::Duration,
};

use futures::{Stream, StreamExt};
use gateway_core::account::OutboundProxy;
use provider_openai::transport::{
    CodexBackendClient, CodexClientError,
    client::build_account_http_client_with_timeout,
    profile::{CodexWireProfile, CodexWireProfileState},
};
use reqwest::header::{CONTENT_ENCODING, CONTENT_LENGTH, HeaderMap, HeaderName, HeaderValue};
use tokio::{
    io::AsyncReadExt,
    net::TcpListener,
    sync::{mpsc, oneshot},
};
use tokio_stream::wrappers::{ReceiverStream, TcpListenerStream};
use tokio_util::sync::CancellationToken;
use tonic::{
    Request, Response, Status, Streaming,
    transport::{Endpoint, Server},
};

mod wire {
    tonic::include_proto!("sub2api.accountdriver.v1");
}
mod websocket;
use wire::{
    ExecuteCommand, ExecuteEvent, ExecuteStart, Header, SendState, execute_command, execute_event,
};

const MAX_BODY_BYTES: usize = 256 * 1024 * 1024;
const BODY_CHUNK_BYTES: usize = 64 * 1024;
const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);

// Only fixed diagnostic labels and OS error codes cross the RPC boundary.
// Network error strings can contain the URL or proxy credentials.
fn http_transport_diagnostic(error: &reqwest::Error) -> String {
    let phase = if error.is_connect() {
        "connect"
    } else {
        "request"
    };
    let mut causes = Vec::new();
    if error.is_timeout() {
        causes.push("timeout".to_string());
    }
    let mut source = error.source();
    for _ in 0..12 {
        let Some(current) = source else { break };
        let message = current.to_string().to_ascii_lowercase();
        let label = if message.contains("dns")
            || message.contains("resolve")
            || message.contains("name or service not known")
        {
            Some("DNS resolution failed")
        } else if message.contains("certificate") {
            Some("TLS certificate failed")
        } else if message.contains("tls") || message.contains("ssl") {
            Some("TLS handshake failed")
        } else if message.contains("authentication")
            || message.contains("unauthorized")
            || message.contains("407")
        {
            Some("proxy authentication failed")
        } else if message.contains("socks")
            || message.contains("proxy")
            || message.contains("tunnel")
        {
            Some("proxy connection failed")
        } else if message.contains("http2")
            || message.contains("http/2")
            || message.starts_with("h2 ")
        {
            Some("HTTP/2 connection failed")
        } else if message.contains("connection closed") || message.contains("incomplete message") {
            Some("connection closed before headers")
        } else {
            None
        };
        if let Some(label) = label {
            causes.push(label.to_string());
        }
        if let Some(io_error) = current.downcast_ref::<std::io::Error>() {
            causes.push(format!("io={:?}", io_error.kind()));
            if let Some(code) = io_error.raw_os_error() {
                causes.push(format!("os_error={code}"));
            }
        }
        source = current.source();
    }
    causes.sort();
    causes.dedup();
    if causes.is_empty() {
        causes.push("network transport failed".into());
    }
    format!("upstream HTTP {phase} failed: {}", causes.join(", "))
}

fn transport_diagnostic(error: CodexClientError) -> String {
    match error {
        CodexClientError::Http(error) => http_transport_diagnostic(&error),
        CodexClientError::ConnectionBudgetExhausted => {
            "upstream connection budget exhausted".into()
        }
        _ => "upstream HTTP transport initialization failed".into(),
    }
}

fn task_diagnostic(error: tokio::task::JoinError) -> String {
    if error.is_panic() {
        "upstream HTTP transport task panicked".into()
    } else {
        "upstream HTTP transport task cancelled".into()
    }
}

#[derive(Clone)]
struct Runner {
    token: String,
    callback: String,
    shutdown: CancellationToken,
}
type Events = Pin<Box<dyn Stream<Item = Result<ExecuteEvent, Status>> + Send>>;

fn event(state: SendState, frame: execute_event::Frame) -> ExecuteEvent {
    ExecuteEvent {
        send_state: state as i32,
        frame: Some(frame),
    }
}
fn failure(state: SendState, code: &str, message: &str) -> ExecuteEvent {
    event(
        state,
        execute_event::Frame::Failure(wire::AttemptFailure {
            code: code.into(),
            message: message.into(),
            ..Default::default()
        }),
    )
}
async fn emit(
    tx: &mpsc::Sender<Result<ExecuteEvent, Status>>,
    cancel: &CancellationToken,
    value: ExecuteEvent,
) -> bool {
    tokio::select! { biased; _ = cancel.cancelled() => false, result = tx.send(Ok(value)) => result.is_ok() }
}
async fn fail(
    tx: &mpsc::Sender<Result<ExecuteEvent, Status>>,
    cancel: &CancellationToken,
    state: SendState,
    code: &str,
    message: &str,
) {
    let _ = emit(tx, cancel, failure(state, code, message)).await;
}

#[tonic::async_trait]
impl wire::account_driver_server::AccountDriver for Runner {
    type ExecuteStream = Events;
    type OpenWebSocketStream = websocket::Events;

    async fn open_web_socket(
        &self,
        request: Request<Streaming<wire::WebSocketCommand>>,
    ) -> Result<Response<Self::OpenWebSocketStream>, Status> {
        websocket::open(self.clone(), request).await
    }

    async fn execute(
        &self,
        request: Request<Streaming<ExecuteCommand>>,
    ) -> Result<Response<Events>, Status> {
        if request
            .metadata()
            .get("authorization")
            .and_then(|v| v.to_str().ok())
            != Some(self.token.as_str())
        {
            return Err(Status::unauthenticated("driver authentication required"));
        }
        let mut commands = request.into_inner();
        let start = match commands.message().await?.and_then(|v| v.frame) {
            Some(execute_command::Frame::Start(start)) => *start,
            _ => {
                return Ok(Response::new(Box::pin(tokio_stream::once(Ok(failure(
                    SendState::NotSent,
                    "invalid_start",
                    "first frame must select one account",
                ))))));
            }
        };
        let cancel = self.shutdown.child_token();
        let input_cancel = cancel.clone();
        let (permit_tx, permit_rx) = oneshot::channel();
        tokio::spawn(async move {
            let mut permit_tx = Some(permit_tx);
            loop {
                let command = tokio::select! { biased; _ = input_cancel.cancelled() => break, value = commands.message() => value };
                match command {
                    Ok(Some(ExecuteCommand {
                        frame: Some(execute_command::Frame::PermitSend(true)),
                    })) => {
                        if let Some(tx) = permit_tx.take() {
                            let _ = tx.send(());
                        }
                    }
                    _ => {
                        input_cancel.cancel();
                        break;
                    }
                }
            }
        });
        let (tx, rx) = mpsc::channel(2);
        let runner = self.clone();
        tokio::spawn(async move {
            runner.attempt(start, permit_rx, cancel, tx).await;
        });
        Ok(Response::new(Box::pin(ReceiverStream::new(rx))))
    }
}

fn validate_start(start: &ExecuteStart) -> Result<reqwest::Url, (&'static str, &'static str)> {
    if start.method != "POST" {
        return Err(("unsupported_method", "driver supports POST only"));
    }
    if start.account_id <= 0
        || start.attempt_id.is_empty()
        || start.request_id.is_empty()
        || start.binding_revision < 0
        || start.credential_revision < 0
    {
        return Err((
            "invalid_identity",
            "selected account and request identity are required",
        ));
    }
    if start.body.len() > MAX_BODY_BYTES || start.timeout_ms < 0 || start.header_timeout_ms < 0 {
        return Err((
            "invalid_shape",
            "request body or timeout exceeds the contract",
        ));
    }
    let url = reqwest::Url::parse(&start.url)
        .map_err(|_| ("unsupported_endpoint", "invalid upstream URL"))?;
    let loopback = url
        .host_str()
        .and_then(|v| v.parse::<std::net::IpAddr>().ok())
        .is_some_and(|v| v.is_loopback());
    let target = (url.scheme() == "https" && url.host_str() == Some("chatgpt.com"))
        || (url.scheme() == "http" && loopback);
    if !target
        || !matches!(
            url.path(),
            "/codex/responses" | "/backend-api/codex/responses"
        )
        || !url.username().is_empty()
        || url.password().is_some()
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return Err((
            "unsupported_endpoint",
            "driver supports Codex Responses on ChatGPT or a loopback fixture only",
        ));
    }
    Ok(url)
}

fn request_headers(start: &[Header], protected: &[Header]) -> Result<HeaderMap, &'static str> {
    let mut protected_names = HashSet::new();
    let mut result = HeaderMap::new();
    for header in protected {
        let name = HeaderName::from_bytes(header.name.as_bytes())
            .map_err(|_| "invalid protected header")?;
        if !matches!(name.as_str(), "authorization" | "chatgpt-account-id")
            || !protected_names.insert(name.clone())
        {
            return Err(
                "identity callback must supply one value for each protected identity header",
            );
        }
        let value =
            HeaderValue::from_bytes(&header.value).map_err(|_| "invalid protected header value")?;
        result.append(name, value);
    }
    if !protected_names.contains(&reqwest::header::AUTHORIZATION) {
        return Err("identity callback omitted authorization");
    }
    for header in start {
        let name =
            HeaderName::from_bytes(header.name.as_bytes()).map_err(|_| "invalid request header")?;
        if matches!(name.as_str(), "authorization" | "chatgpt-account-id") {
            continue;
        }
        if matches!(
            name.as_str(),
            "connection" | "proxy-connection" | "transfer-encoding" | "upgrade"
        ) {
            return Err("host must remove connection framing headers before driver execution");
        }
        let value =
            HeaderValue::from_bytes(&header.value).map_err(|_| "invalid request header value")?;
        result.append(name, value);
    }
    Ok(result)
}

impl Runner {
    async fn attempt(
        &self,
        start: ExecuteStart,
        permit: oneshot::Receiver<()>,
        cancel: CancellationToken,
        tx: mpsc::Sender<Result<ExecuteEvent, Status>>,
    ) {
        let _cancel_input_on_exit = cancel.clone().drop_guard();
        let url = match validate_start(&start) {
            Ok(v) => v,
            Err((code, message)) => {
                fail(&tx, &cancel, SendState::NotSent, code, message).await;
                return;
            }
        };
        let endpoint = match Endpoint::from_shared(self.callback.clone()) {
            Ok(v) => v.connect_timeout(CONNECT_TIMEOUT).timeout(CONNECT_TIMEOUT),
            Err(_) => {
                fail(
                    &tx,
                    &cancel,
                    SendState::NotSent,
                    "callback_configuration",
                    "host callback endpoint is invalid",
                )
                .await;
                return;
            }
        };
        let connected = tokio::select! {
            biased;
            _ = cancel.cancelled() => return,
            value = endpoint.connect() => value,
        };
        let channel = match connected {
            Ok(v) => v,
            Err(_) => {
                fail(
                    &tx,
                    &cancel,
                    SendState::NotSent,
                    "callback_unavailable",
                    "host identity callback could not be reached",
                )
                .await;
                return;
            }
        };
        let mut callback = wire::host_callbacks_client::HostCallbacksClient::new(channel);
        let mut identity_request = Request::new(wire::IdentityRequest {
            request_id: start.request_id.clone(),
            attempt_id: start.attempt_id.clone(),
            account_id: start.account_id,
            credential_revision: start.credential_revision,
            binding_revision: start.binding_revision,
            ..Default::default()
        });
        identity_request.metadata_mut().insert(
            "authorization",
            self.token.parse().expect("validated process token"),
        );
        let identity_result = tokio::select! { biased; _ = cancel.cancelled() => return, value = callback.resolve_identity(identity_request) => value };
        let identity = match identity_result {
            Ok(v) if v.get_ref().authorized => v.into_inner(),
            _ => {
                fail(
                    &tx,
                    &cancel,
                    SendState::NotSent,
                    "identity_rejected",
                    "host denied this account, binding or credential revision",
                )
                .await;
                return;
            }
        };
        if identity.proxy_url != start.proxy_url {
            fail(
                &tx,
                &cancel,
                SendState::NotSent,
                "proxy_identity_mismatch",
                "host proxy identity changed",
            )
            .await;
            return;
        }
        let mut headers = match request_headers(&start.headers, &identity.protected_headers) {
            Ok(v) => v,
            Err(message) => {
                fail(&tx, &cancel, SendState::NotSent, "invalid_headers", message).await;
                return;
            }
        };
        let proxy = if identity.proxy_url.is_empty() {
            None
        } else {
            match OutboundProxy::parse(&identity.proxy_url) {
                Ok(v) => Some(v),
                Err(_) => {
                    fail(
                        &tx,
                        &cancel,
                        SendState::NotSent,
                        "invalid_proxy",
                        "host proxy configuration is invalid",
                    )
                    .await;
                    return;
                }
            }
        };
        let client = match build_account_http_client_with_timeout(
            &start.account_id.to_string(),
            proxy.as_ref(),
            CONNECT_TIMEOUT,
        ) {
            Ok(v) => v,
            Err(_) => {
                fail(
                    &tx,
                    &cancel,
                    SendState::NotSent,
                    "transport_configuration",
                    "CPR HTTP transport could not initialize",
                )
                .await;
                return;
            }
        };
        let mut body = start.body;
        let codex_responses = url.scheme() == "https"
            && url.host_str() == Some("chatgpt.com")
            && matches!(
                url.path(),
                "/codex/responses" | "/backend-api/codex/responses"
            );
        if codex_responses {
            body = match zstd::stream::encode_all(Cursor::new(body), 3) {
                Ok(value) => value,
                Err(_) => {
                    fail(
                        &tx,
                        &cancel,
                        SendState::NotSent,
                        "request_compression",
                        "Codex request compression failed",
                    )
                    .await;
                    return;
                }
            };
            headers.remove(CONTENT_LENGTH);
            headers.remove(CONTENT_ENCODING);
            headers.insert(CONTENT_ENCODING, HeaderValue::from_static("zstd"));
        }
        let mut outbound = match client.post(url.clone()).headers(headers).body(body).build() {
            Ok(v) => v,
            Err(_) => {
                fail(
                    &tx,
                    &cancel,
                    SendState::NotSent,
                    "request_configuration",
                    "prepared HTTP request is invalid",
                )
                .await;
                return;
            }
        };
        if start.timeout_ms > 0 {
            *outbound.timeout_mut() = Some(Duration::from_millis(start.timeout_ms as u64));
        }
        let transport = CodexBackendClient::new(
            client,
            url.as_str(),
            CodexWireProfileState::new(CodexWireProfile::default()),
        );
        if !emit(
            &tx,
            &cancel,
            event(
                SendState::NotSent,
                execute_event::Frame::Ready(wire::Ready {}),
            ),
        )
        .await
        {
            return;
        }
        let allowed =
            tokio::select! { biased; _ = cancel.cancelled() => false, v = permit => v.is_ok() };
        if !allowed {
            let _ = tx.try_send(Ok(failure(
                SendState::NotSent,
                "cancelled",
                "attempt cancelled before send permission",
            )));
            return;
        }
        // 许可前宿主已持有 ambiguous；许可后不回落到 not_sent
        if !emit(
            &tx,
            &cancel,
            event(
                SendState::Ambiguous,
                execute_event::Frame::StateChanged(wire::StateChanged {}),
            ),
        )
        .await
        {
            return;
        }
        enum HeaderResult {
            Response(reqwest::Response),
            Timeout,
            Transport(String),
        }
        let mut request_task =
            tokio::spawn(async move { transport.execute_prepared_http(outbound).await });
        // JoinHandle 只借用到等待分支；取消与超时都先 abort 再等待任务结束
        let result = if start.header_timeout_ms > 0 {
            tokio::select! {
                biased;
                _ = cancel.cancelled() => {
                    request_task.abort();
                    let _ = request_task.await;
                    return;
                },
                result = tokio::time::timeout(
                    Duration::from_millis(start.header_timeout_ms as u64),
                    &mut request_task,
                ) => match result {
                    Ok(Ok(Ok(response))) => HeaderResult::Response(response),
                    Ok(Ok(Err(error))) => HeaderResult::Transport(transport_diagnostic(error)),
                    Ok(Err(error)) => HeaderResult::Transport(task_diagnostic(error)),
                    Err(_) => {
                        request_task.abort();
                        let _ = request_task.await;
                        HeaderResult::Timeout
                    }
                },
            }
        } else {
            tokio::select! {
                biased;
                _ = cancel.cancelled() => {
                    request_task.abort();
                    let _ = request_task.await;
                    return;
                },
                result = &mut request_task => match result {
                    Ok(Ok(response)) => HeaderResult::Response(response),
                    Ok(Err(error)) => HeaderResult::Transport(transport_diagnostic(error)),
                    Err(error) => HeaderResult::Transport(task_diagnostic(error)),
                },
            }
        };
        let response = match result {
            HeaderResult::Response(v) => v,
            HeaderResult::Timeout => {
                fail(
                    &tx,
                    &cancel,
                    SendState::Ambiguous,
                    "response_header_timeout",
                    "upstream response headers exceeded the attempt header timeout",
                )
                .await;
                return;
            }
            HeaderResult::Transport(message) => {
                fail(
                    &tx,
                    &cancel,
                    SendState::Ambiguous,
                    "upstream_transport",
                    &message,
                )
                .await;
                return;
            }
        };
        let response_headers = response
            .headers()
            .iter()
            .map(|(name, value)| Header {
                name: name.as_str().into(),
                value: value.as_bytes().to_vec(),
            })
            .collect();
        let protocol = match response.version() {
            reqwest::Version::HTTP_10 => "HTTP/1.0",
            reqwest::Version::HTTP_11 => "HTTP/1.1",
            reqwest::Version::HTTP_2 => "HTTP/2.0",
            _ => "HTTP/unknown",
        };
        let head = wire::ResponseHead {
            status_code: u32::from(response.status().as_u16()),
            headers: response_headers,
            protocol: protocol.into(),
        };
        if !emit(
            &tx,
            &cancel,
            event(SendState::Sent, execute_event::Frame::Head(head)),
        )
        .await
        {
            return;
        }
        let mut body = response.bytes_stream();
        let mut bytes_received = 0u64;
        loop {
            let chunk = tokio::select! { biased; _ = cancel.cancelled() => return, value = body.next() => value };
            match chunk {
                Some(Ok(chunk)) => {
                    bytes_received = bytes_received.saturating_add(chunk.len() as u64);
                    for part in chunk.chunks(BODY_CHUNK_BYTES) {
                        if !emit(
                            &tx,
                            &cancel,
                            event(
                                SendState::Sent,
                                execute_event::Frame::Body(wire::BodyChunk {
                                    data: part.to_vec(),
                                }),
                            ),
                        )
                        .await
                        {
                            return;
                        }
                    }
                }
                Some(Err(error)) => {
                    fail(
                        &tx,
                        &cancel,
                        SendState::Sent,
                        "upstream_stream",
                        &http_transport_diagnostic(&error),
                    )
                    .await;
                    return;
                }
                None => {
                    let _ = emit(
                        &tx,
                        &cancel,
                        event(
                            SendState::Sent,
                            execute_event::Frame::End(wire::ResponseEnd { bytes_received }),
                        ),
                    )
                    .await;
                    return;
                }
            }
        }
    }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let token = env::var("SUB2API_ACCOUNT_DRIVER_TOKEN")?;
    if token.len() < 32
        || token
            .parse::<tonic::metadata::MetadataValue<tonic::metadata::Ascii>>()
            .is_err()
    {
        return Err("invalid driver process token".into());
    }
    let callback = env::var("SUB2API_ACCOUNT_DRIVER_CALLBACK")?;
    let callback_url = reqwest::Url::parse(&callback)?;
    if callback_url.scheme() != "http"
        || callback_url.host_str() != Some("127.0.0.1")
        || !callback_url.username().is_empty()
        || callback_url.password().is_some()
    {
        return Err("authenticated loopback callback required".into());
    }
    let listener = TcpListener::bind("127.0.0.1:0").await?;
    let address = listener.local_addr()?;
    let shutdown = CancellationToken::new();
    let stdin_shutdown = shutdown.clone();
    tokio::spawn(async move {
        let mut stdin = tokio::io::stdin();
        let mut byte = [0u8; 1];
        loop {
            if !matches!(stdin.read(&mut byte).await, Ok(n) if n > 0) {
                stdin_shutdown.cancel();
                break;
            }
        }
    });
    println!(
        "{}",
        serde_json::json!({"protocol":"sub2api-account-driver-v1","api_version":1,"address":address.to_string(),"driver_id":"codex-rs-http","driver_version":env!("CARGO_PKG_VERSION"),"upstream_commit":env!("CPR_UPSTREAM_COMMIT"),"transports":["http","sse","websocket"]})
    );
    std::io::stdout().flush()?;
    let driver = wire::account_driver_server::AccountDriverServer::new(Runner {
        token,
        callback,
        shutdown: shutdown.clone(),
    })
    .max_decoding_message_size(MAX_BODY_BYTES + 1024 * 1024)
    .max_encoding_message_size(websocket::MAX_MESSAGE_BYTES + 4096);
    Server::builder()
        .add_service(driver)
        .serve_with_incoming_shutdown(TcpListenerStream::new(listener), shutdown.cancelled())
        .await?;
    Ok(())
}
