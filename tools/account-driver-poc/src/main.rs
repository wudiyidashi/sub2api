//! 宿主指定账号的一次 CPR HTTP/SSE 执行与受管 gRPC 进程验证

use futures::{Stream, StreamExt};
use gateway_protocol::openai::sse::SseEventDecoder;
use provider_openai::transport::{
    CodexBackendClient, CodexClientError, CodexRequestContext, CodexUpstreamSendPhase,
    profile::{CodexWireProfile, CodexWireProfileState},
    protocol::responses::CodexResponsesRequest,
};
use std::{env, io::Write, pin::Pin, time::Duration};
use tokio::{
    io::AsyncReadExt,
    net::TcpListener,
    sync::{mpsc, oneshot},
};
use tokio_stream::wrappers::{ReceiverStream, TcpListenerStream};
use tokio_util::sync::CancellationToken;
use tonic::{Request, Response, Status, Streaming, transport::Server};

mod wire {
    tonic::include_proto!("accountdriver.poc.v1");
}
use wire::{Command, Event, Start, command::Frame};

#[derive(Clone)]
struct Runner {
    token: String,
    callback: String,
    shutdown: CancellationToken,
}

type Events = Pin<Box<dyn Stream<Item = Result<Event, Status>> + Send>>;

#[tonic::async_trait]
impl wire::driver_server::Driver for Runner {
    type ExecuteStream = Events;

    async fn execute(
        &self,
        request: Request<Streaming<Command>>,
    ) -> Result<Response<Events>, Status> {
        if request
            .metadata()
            .get("authorization")
            .and_then(|v| v.to_str().ok())
            != Some(self.token.as_str())
        {
            return Err(Status::unauthenticated("runner authentication required"));
        }
        let mut commands = request.into_inner();
        let start = match commands.message().await?.and_then(|v| v.frame) {
            Some(Frame::Start(start)) => start,
            _ => {
                return Err(Status::invalid_argument(
                    "first frame must select one account",
                ));
            }
        };
        let url = reqwest::Url::parse(&start.base_url)
            .map_err(|_| Status::invalid_argument("base URL"))?;
        let loopback = url
            .host_str()
            .and_then(|v| v.parse::<std::net::IpAddr>().ok())
            .is_some_and(|v| v.is_loopback());
        if url.scheme() != "http"
            || !loopback
            || !url.username().is_empty()
            || url.password().is_some()
            || url.query().is_some()
            || url.fragment().is_some()
        {
            return Err(Status::invalid_argument(
                "PoC accepts only loopback fake upstream",
            ));
        }
        let value: serde_json::Value = serde_json::from_slice(&start.body)
            .map_err(|_| Status::invalid_argument("JSON body"))?;
        let body = value
            .as_object()
            .cloned()
            .ok_or_else(|| Status::invalid_argument("JSON object required"))?;
        let mut upstream_request = CodexResponsesRequest::from_body(body);
        upstream_request.force_http_sse = true;

        let cancel = self.shutdown.child_token();
        let input_cancel = cancel.clone();
        let (permit_tx, permit_rx) = oneshot::channel();
        tokio::spawn(async move {
            let mut permit_tx = Some(permit_tx);
            loop {
                let command = tokio::select! {
                    biased;
                    _ = input_cancel.cancelled() => break,
                    value = commands.message() => value,
                };
                match command {
                    Ok(Some(Command {
                        frame: Some(Frame::PermitSend(true)),
                    })) => {
                        if let Some(tx) = permit_tx.take() {
                            let _ = tx.send(());
                        }
                    }
                    Ok(Some(Command {
                        frame: Some(Frame::Cancel(true)),
                    }))
                    | Err(_)
                    | Ok(None) => {
                        input_cancel.cancel();
                        break;
                    }
                    _ => {
                        input_cancel.cancel();
                        break;
                    }
                }
            }
        });
        // 有界输出避免慢宿主造成无限内存，取消不依赖输出队列腾出空间
        let (tx, rx) = mpsc::channel(2);
        let runner = self.clone();
        tokio::spawn(async move {
            runner
                .attempt(start, upstream_request, permit_rx, cancel, tx)
                .await;
        });
        Ok(Response::new(Box::pin(ReceiverStream::new(rx))))
    }
}

fn event(kind: &str, state: &str) -> Event {
    Event {
        kind: kind.into(),
        send_state: state.into(),
        ..Default::default()
    }
}

async fn emit(
    tx: &mpsc::Sender<Result<Event, Status>>,
    cancel: &CancellationToken,
    value: Event,
) -> bool {
    tokio::select! {
        biased;
        _ = cancel.cancelled() => false,
        result = tx.send(Ok(value)) => result.is_ok(),
    }
}

impl Runner {
    async fn attempt(
        &self,
        start: Start,
        request: CodexResponsesRequest,
        permit: oneshot::Receiver<()>,
        cancel: CancellationToken,
        tx: mpsc::Sender<Result<Event, Status>>,
    ) {
        let _cancel_input_on_exit = cancel.clone().drop_guard();
        let connected = tokio::select! {
            biased;
            _ = cancel.cancelled() => { let _ = tx.try_send(Ok(event("cancelled", "not_sent"))); return; },
            value = wire::host_callbacks_client::HostCallbacksClient::connect(self.callback.clone()) => value,
        };
        let mut callback = match connected {
            Ok(v) => v,
            Err(_) => {
                let mut value = event("failed", "not_sent");
                value.error_code = "callback_unavailable".into();
                let _ = emit(&tx, &cancel, value).await;
                return;
            }
        };
        let mut identity_request = Request::new(start.clone());
        identity_request.metadata_mut().insert(
            "authorization",
            self.token.parse().expect("validated token"),
        );
        let identity = tokio::select! {
            biased;
            _ = cancel.cancelled() => { let _ = tx.try_send(Ok(event("cancelled", "not_sent"))); return; },
            result = callback.resolve_identity(identity_request) => match result {
                Ok(v) => v.into_inner(),
                Err(_) => { let mut value = event("failed", "not_sent"); value.error_code = "identity_rejected".into(); let _ = emit(&tx, &cancel, value).await; return; }
            }
        };
        let client = match reqwest::Client::builder()
            .no_proxy()
            .redirect(reqwest::redirect::Policy::none())
            .connect_timeout(Duration::from_secs(2))
            .build()
        {
            Ok(v) => v,
            Err(_) => {
                let mut value = event("failed", "not_sent");
                value.error_code = "client_configuration".into();
                let _ = emit(&tx, &cancel, value).await;
                return;
            }
        };
        let transport = CodexBackendClient::new(
            client,
            &start.base_url,
            CodexWireProfileState::new(CodexWireProfile::default()),
        );
        let context = CodexRequestContext::auxiliary(
            &identity.authorization,
            Some(&identity.upstream_account_id),
            &start.attempt_id,
            None,
        );
        if !emit(&tx, &cancel, event("ready", "not_sent")).await {
            let _ = tx.try_send(Ok(event("cancelled", "not_sent")));
            return;
        }
        let allowed =
            tokio::select! { biased; _ = cancel.cancelled() => false, v = permit => v.is_ok() };
        if !allowed {
            let _ = tx.try_send(Ok(event("cancelled", "not_sent")));
            return;
        }
        // 宿主必须先记为 ambiguous 再发送 permit，防止发送事件在进程崩溃时丢失
        if !emit(&tx, &cancel, event("sending", "ambiguous")).await {
            return;
        }
        let result = tokio::select! {
            biased;
            _ = cancel.cancelled() => { let _ = tx.try_send(Ok(event("cancelled", "ambiguous"))); return; },
            result = transport.create_response_stream_with_pool_account(&request, context, None) => result,
        };
        let mut response = match result {
            Ok(v) => v,
            Err(error) => {
                let mut value = event("failed", "ambiguous");
                value.error_code = "upstream_failed".into();
                match error {
                    CodexClientError::Upstream {
                        status,
                        client_response,
                        send_phase,
                        ..
                    } => {
                        value.send_state = match send_phase {
                            CodexUpstreamSendPhase::BeforePayload => "ambiguous",
                            CodexUpstreamSendPhase::AfterPayload => "sent",
                            CodexUpstreamSendPhase::Ambiguous => "ambiguous",
                        }
                        .into();
                        value.status = u32::from(status.as_u16());
                        value.body = client_response
                            .map(|v| v.body().to_vec())
                            .unwrap_or_default();
                    }
                    CodexClientError::ErrorBodyRead { status, .. } => {
                        value.send_state = "sent".into();
                        value.status = u32::from(status.as_u16());
                    }
                    _ => {}
                }
                let _ = emit(&tx, &cancel, value).await;
                return;
            }
        };
        let mut header = event("headers", "sent");
        header.status = u32::from(response.diagnostics.status_code.unwrap_or(200));
        if !emit(&tx, &cancel, header).await {
            return;
        }
        let mut decoder = SseEventDecoder::default();
        loop {
            let chunk = tokio::select! {
                biased;
                _ = cancel.cancelled() => { let _ = tx.try_send(Ok(event("cancelled", "sent"))); return; },
                chunk = response.body.next() => chunk,
            };
            match chunk {
                Some(Ok(chunk)) => {
                    let mut value = event("body", "sent");
                    value.body = chunk.to_vec();
                    if !emit(&tx, &cancel, value).await {
                        return;
                    }
                    for frame in decoder.push_frames(&chunk) {
                        for observed in frame.events() {
                            let Ok(value) =
                                serde_json::from_str::<serde_json::Value>(&observed.data)
                            else {
                                continue;
                            };
                            if let Some(usage) = value.pointer("/response/usage") {
                                let mut value = event("usage", "sent");
                                value.input_tokens = usage
                                    .get("input_tokens")
                                    .and_then(|v| v.as_u64())
                                    .unwrap_or(0);
                                value.output_tokens = usage
                                    .get("output_tokens")
                                    .and_then(|v| v.as_u64())
                                    .unwrap_or(0);
                                if !emit(&tx, &cancel, value).await {
                                    return;
                                }
                            }
                        }
                    }
                }
                Some(Err(_)) => {
                    let mut value = event("failed", "sent");
                    value.error_code = "stream_failed".into();
                    let _ = emit(&tx, &cancel, value).await;
                    return;
                }
                None => {
                    let _ = emit(&tx, &cancel, event("end", "sent")).await;
                    return;
                }
            }
        }
    }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let token = env::var("ACCOUNT_DRIVER_POC_TOKEN")?;
    if token.len() < 32
        || token
            .parse::<tonic::metadata::MetadataValue<tonic::metadata::Ascii>>()
            .is_err()
    {
        return Err("invalid runner token".into());
    }
    let callback = env::var("ACCOUNT_DRIVER_POC_CALLBACK")?;
    let callback_url = reqwest::Url::parse(&callback)?;
    if callback_url.scheme() != "http" || callback_url.host_str() != Some("127.0.0.1") {
        return Err("loopback callback required".into());
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
        serde_json::json!({"protocol":"account-driver-poc-v1","address":address.to_string(),"transport":"grpc","upstream_commit":"d875a62652e278eb5beef54356e594d1d1149e37"})
    );
    std::io::stdout().flush()?;
    Server::builder()
        .add_service(wire::driver_server::DriverServer::new(Runner {
            token,
            callback,
            shutdown: shutdown.clone(),
        }))
        .serve_with_incoming_shutdown(TcpListenerStream::new(listener), shutdown.cancelled())
        .await?;
    Ok(())
}
