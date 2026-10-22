import { spawnSync } from 'node:child_process'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('.', import.meta.url))
const frontend = fileURLToPath(new URL('./.upstream/build/frontend/', import.meta.url))
const windows = process.platform === 'win32'
if (Number(process.versions.node.split('.')[0]) < 24) throw new Error('CPR account console requires Node.js 24 or newer')
function run(command, args, cwd = root) {
  // shell 只接收固定命令和静态参数，源码路径通过环境变量传给 Python。
  const result = spawnSync(command, args, { cwd, stdio: 'inherit', shell: windows })
  if (result.status !== 0) process.exit(result.status || 1)
}
const action = process.argv[2] || 'build'
if (!['install', 'typecheck', 'build', 'dev'].includes(action)) throw new Error('Unsupported CPR console command')
run(windows ? 'python' : 'python3', ['prepare.py'])
const installState = new URL('./.upstream/install-state', import.meta.url)
const fingerprint = createHash('sha256').update(readFileSync(new URL('./.upstream/build/frontend/package.json', import.meta.url))).update(readFileSync(new URL('./.upstream/build/frontend/pnpm-lock.yaml', import.meta.url))).update(readFileSync(new URL('./.upstream/build/frontend/pnpm-workspace.yaml', import.meta.url))).update(process.versions.node.split('.')[0]).digest('hex')
if (action === 'install' || !existsSync(installState) || readFileSync(installState, 'utf8') !== fingerprint || !existsSync(new URL('./.upstream/build/frontend/node_modules/.modules.yaml', import.meta.url))) {
  run('npx', ['--yes', 'pnpm@12.6.0', 'install', '--frozen-lockfile'], frontend)
  writeFileSync(installState, fingerprint)
}
if (action !== 'install') {
  run('npx', ['--yes', 'pnpm@12.6.0', 'run', action], frontend)
}
