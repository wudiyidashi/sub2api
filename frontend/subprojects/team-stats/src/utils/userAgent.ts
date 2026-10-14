/**
 * 把 usage 日志里的 user_agent 归类成"工具名"。
 * 规则按特异性排序：越具体的客户端越靠前，通用 HTTP 库和浏览器兜底。
 */

const RULES: Array<[RegExp, string]> = [
  [/claude[-_ ]?(cli|code)/i, 'Claude Code'],
  [/codex/i, 'Codex CLI'],
  [/gemini[-_ ]?cli/i, 'Gemini CLI'],
  [/opencode/i, 'OpenCode'],
  [/roo[-_ ]?code/i, 'Roo Code'],
  [/kilo[-_ ]?code/i, 'Kilo Code'],
  [/\bcline\b/i, 'Cline'],
  [/cherry[-_ ]?studio/i, 'Cherry Studio'],
  [/lobe[-_ ]?chat|lobehub/i, 'LobeChat'],
  [/nextchat|chatgpt-next-web/i, 'NextChat'],
  [/chatbox/i, 'Chatbox'],
  [/\bcursor\b/i, 'Cursor'],
  [/windsurf/i, 'Windsurf'],
  [/\bcontinue\b/i, 'Continue'],
  [/\bzed\b/i, 'Zed'],
  [/jetbrains|intellij|pycharm|webstorm|goland/i, 'JetBrains IDE'],
  [/vscode|visual[-_ ]?studio[-_ ]?code/i, 'VS Code 插件'],
  [/anthropic[-_/]/i, 'Anthropic SDK'],
  [/openai[-_/]/i, 'OpenAI SDK'],
  [/new[-_]?api|one[-_]?api/i, 'API 网关中转'],
  [/python-requests|aiohttp|httpx/i, 'Python 脚本'],
  [/axios|node-fetch|undici|got\//i, 'Node 脚本'],
  [/curl\//i, 'curl'],
  [/okhttp|java\//i, 'Java/OkHttp'],
  [/\bgo-http-client\b/i, 'Go 脚本'],
  [/mozilla/i, '浏览器'],
];

export function classifyUserAgent(ua?: string | null): string {
  const s = (ua || '').trim();
  if (!s) return '未知';
  for (const [re, name] of RULES) {
    if (re.test(s)) return name;
  }
  return '其他';
}
