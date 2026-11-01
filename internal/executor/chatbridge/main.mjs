import {Gateway, failure, submissionError} from './gateway.mjs';
import {ConnectionState} from './connection.mjs';
import {PromptMonitor} from './prompt-state.mjs';
import {StreamState, byteSize, historyPage, messageBytes, fragmentBytes, inputReceipt, observation, visibleHistory, visibleBodyState, HISTORY_MAX_CHARS, FULL_MESSAGE_MAX_CHARS} from './projection.mjs';

const streams = new StreamState();
const connection = new ConnectionState();
const gateway = new Gateway(event => { streams.event(event); connection.event(event); }, () => process.exit(2));
let bootstrapped = false;
let promptMonitor;
let inflight = 0;
let input = '';
// A single bounded, short-lived read cache avoids fetching the entire native
// message for every 8 KiB fragment. It is never persisted or used as authority.
let fullMessage = null;
const validID = value => typeof value === 'string' && /^[A-Za-z0-9_-]{16,128}$/.test(value);
const sessionKey = binding => `agent:main:asterhand:${binding.key}`;

async function readHistory(binding, extra = {}) {
  const result = await gateway.readyRead('chat.history', {sessionKey: sessionKey(binding), limit: 50, maxChars: HISTORY_MAX_CHARS, maxBytes: 65536, ...extra});
  if (binding.session && (result.sessionId !== binding.session || result.sessionInfo?.lifecycleRevision !== binding.lifecycle)) {
    throw failure('binding_changed', '原生会话身份或生命周期已变化，已停止访问原绑定');
  }
  return result;
}

async function create(binding) {
  await gateway.readyRead('sessions.create', {key: sessionKey(binding)});
  const history = await readHistory(binding);
  if (!history.sessionId || !history.sessionInfo?.lifecycleRevision) throw failure('unavailable', '会话绑定尚未确认');
  return {...binding, session: history.sessionId, lifecycle: history.sessionInfo.lifecycleRevision};
}

async function submit(command) {
  if (!validID(command.run) || typeof command.text !== 'string' || !command.text.trim() ||
    Buffer.byteLength(command.text) > 16000 || command.text.includes('\r') || command.text.trimStart().startsWith('/')) {
    throw failure('rejected', '聊天文本无效或包含管理命令，草稿已保留');
  }
  let history;
  try { history = await readHistory(command.binding); }
  catch { throw failure('rejected', '发送前会话校验失败，草稿已保留，请刷新核对绑定'); }
  promptMonitor.begin(command.run, command.binding.session);
  const result = await gateway.rpc('chat.send', {
    sessionKey: sessionKey(command.binding), sessionId: command.binding.session, message: command.text,
    idempotencyKey: command.run, deliver: false, timeoutMs: 600000,
    expectedLeafEntryId: history.sessionInfo?.activeLeafEntryId ?? null,
  }, 30000).catch(error => { throw submissionError(error); });
  let receipt = {accepted: Number.isInteger(result.messageSeq) && result.messageSeq > 0, user_message: ''};
  if (!receipt.accepted) {
    try { receipt = inputReceipt(await readHistory(command.binding, {inputRunIds: [command.run]}), command.run); }
    // The send already crossed admission. Read failure leaves delivery unknown;
    // it must never be translated into a pre-send rejection or a second send.
    catch { /* Existing reconciliation will query the original identity again. */ }
  }
  return {...receipt,
    prompt: await promptMonitor.status(command.run, command.binding.session)};
}

async function handle(command) {
  if (command.action === 'bootstrap') {
    if (bootstrapped || typeof command.entry !== 'string' || !Number.isInteger(command.port) || !command.token) {
      throw failure('invalid_request', '运行时初始化参数无效');
    }
    bootstrapped = true;
    promptMonitor = new PromptMonitor(command.prompt, command.prompt_instance, async identity => {
      const result = await gateway.rpc('asterhand.chat.promptStatus', identity, 1000);
      return result.record;
    });
    await gateway.start(command);
    return {};
  }
  if (bootstrapped && ['connect', 'connection'].includes(command.action)) {
    if (command.action === 'connect') void gateway.ensureConnected().catch(() => {});
    return connection.snapshot(gateway);
  }
  const binding = command.binding;
  if (!bootstrapped || !binding || !validID(binding.key) || binding.runtime !== 'openclaw' || !validID(binding.generation)) {
    throw failure('invalid_request', '会话绑定无效');
  }
  await gateway.ensureConnected().catch(error => { throw command.action === 'submit' ? submissionError(error) : error; });
  switch (command.action) {
    case 'create': return create(binding);
    case 'submit': return submit(command);
    case 'history': {
      const offset = command.offset ?? 0;
      if (!Number.isInteger(offset) || offset < 0) throw failure('invalid_request', '历史位置无效');
      const history = await readHistory(binding, {offset});
      const project = value => visibleHistory(value, async (messageId, maxChars) => {
        const result = await gateway.rpc('chat.message.get', {sessionKey: sessionKey(binding),
          sessionId: binding.session, messageId, maxChars});
        return result.ok ? result.message : null;
      });
      const page = historyPage(await project(history), offset, command.cursor ?? '');
      if (!page.resetRequired) return page;
      return {...historyPage(await project(await readHistory(binding)), 0, ''), reset: true};
    }
    case 'message': {
      if (!validID(command.message)) throw failure('invalid_request', '消息身份无效');
      await readHistory(binding, {limit: 1});
      const key = JSON.stringify([binding.key, binding.session, binding.lifecycle, command.message]);
      if (!fullMessage || fullMessage.key !== key || fullMessage.expires < Date.now()) {
        fullMessage = null;
        const result = await gateway.rpc('chat.message.get', {sessionKey: sessionKey(binding), sessionId: binding.session,
          messageId: command.message, maxChars: FULL_MESSAGE_MAX_CHARS});
        if (!result.ok || !result.message || visibleBodyState(result.message, FULL_MESSAGE_MAX_CHARS) !== 'complete' || byteSize(result.message) > 32 * 1024 * 1024) {
          throw failure('message_unavailable', '运行时无法在完整读取上限内返回消息，当前内容为预览');
        }
        fullMessage = {key, bytes: messageBytes(result.message), expires: Date.now() + 30000};
        const entry = fullMessage;
        setTimeout(() => { if (fullMessage === entry) fullMessage = null; }, 30000).unref();
      }
      return fragmentBytes(fullMessage.bytes, command.message, command.offset ?? 0);
    }
    case 'observe': {
      if (!validID(command.run)) throw failure('invalid_request', '轮次身份无效');
      const history = await readHistory(binding, {inputRunIds: [command.run]});
      const result = observation(history, command.run, command.started, gateway.started, streams.get(command.run, sessionKey(binding)));
      return {...result, prompt: await promptMonitor.status(command.run, binding.session, result.stopped)};
    }
    case 'cancel': {
      if (!validID(command.run)) throw failure('invalid_request', '轮次身份无效');
      await readHistory(binding, {limit: 1});
      // Stop this reply without withdrawing its accepted user input. Native
      // discardPendingInput is a queued-input operation and skips running turns.
      await gateway.rpc('chat.abort', {sessionKey: sessionKey(binding), runId: command.run});
      return {};
    }
    default: throw failure('unsupported', '运行时不支持此会话操作');
  }
}

function reply(value) {
  const serialized = JSON.stringify(value);
  if (Buffer.byteLength(serialized) >= 128 * 1024 || process.stdout.writableLength > 1024 * 1024) process.exit(3);
  if (!process.stdout.write(serialized + '\n')) {
    process.stdin.pause();
    process.stdout.once('drain', () => process.stdin.resume());
  }
}

async function dispatch(line) {
  let command;
  try {
    command = JSON.parse(line);
    if (typeof command.id !== 'string' || command.id.length > 32 || ++inflight > 16) process.exit(3);
    const result = await handle(command);
    if (byteSize(result) > 64 * 1024) throw failure('unavailable', '会话结果超过分页限制');
    reply({id: command.id, result});
  } catch (error) {
    if (!command?.id) process.exit(3);
    // Native errors may carry provider diagnostics. Only adapter-authored public
    // messages leave this process; native payloads are never forwarded or logged.
    reply({id: command.id, error: {code: error.publicMessage ? error.code : 'unavailable',
      message: error.publicMessage ? error.message : '会话运行时暂不可用，请核对状态'}});
  } finally {
    inflight--;
  }
}

process.stdin.setEncoding('utf8');
process.stdin.on('data', chunk => {
  input += chunk;
  if (Buffer.byteLength(input) > 128 * 1024) process.exit(3);
  for (;;) {
    const end = input.indexOf('\n');
    if (end < 0) break;
    const line = input.slice(0, end);
    input = input.slice(end + 1);
    void dispatch(line);
  }
});
process.stdin.on('end', () => process.exit(0));
