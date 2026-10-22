"""固定 CPR 前端源码并应用宿主适配文件，保留其独立依赖及完整管理页面。"""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parent
LOCK = json.loads((ROOT / "upstream-lock.json").read_text(encoding="utf-8"))
CONTRACT = json.loads((ROOT.parents[2] / "backend/internal/cpr/management-endpoints.json").read_text(encoding="utf-8"))
REVISION = LOCK["commit"]
if not re.fullmatch(r"[0-9a-f]{40}", REVISION):
    raise SystemExit("Invalid CPR frontend source commit")


def git(source, *args):
    return subprocess.check_output(["git", "-C", str(source), *args], stderr=subprocess.PIPE)


def safe_target(root, relative):
    target = (root / relative).resolve()
    if not target.is_relative_to(root.resolve()):
        raise RuntimeError("CPR frontend source path escapes its build directory")
    return target


def write(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    if not path.exists() or path.read_bytes() != data:
        path.write_bytes(data)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", default=os.environ.get("SUB2API_CPR_SOURCE"))
    args = parser.parse_args()
    cache = ROOT / ".upstream"
    cache.mkdir(exist_ok=True)
    source = Path(args.source).resolve() if args.source else cache / "repository"
    if not args.source:
        if not (source / ".git").is_dir():
            source.mkdir(exist_ok=True)
            subprocess.run(["git", "init", "-q", str(source)], check=True)
            subprocess.run(["git", "-C", str(source), "remote", "add", "origin", LOCK["repository"]], check=True)
        try:
            git(source, "cat-file", "-e", REVISION + "^{commit}")
        except subprocess.CalledProcessError:
            subprocess.run(["git", "-C", str(source), "fetch", "--depth=1", "origin", REVISION], check=True)
    actual = git(source, "rev-parse", REVISION + "^{commit}").decode().strip()
    if actual != REVISION:
        raise SystemExit("CPR frontend source does not match its lock")
    if CONTRACT["source_commit"] != REVISION:
        raise SystemExit("CPR management contract and frontend source must be upgraded together")
    # 路由白名单只允许来自相同固定源码的真实管理入口，源码升级必须逐项复核。
    route_archive = git(source, "archive", "--format=tar", REVISION, "backend/crates/gateway-api/src/admin")
    route_sources = []
    with tarfile.open(fileobj=io.BytesIO(route_archive)) as tar:
        for member in tar:
            if member.isfile():
                stream = tar.extractfile(member)
                if stream is not None:
                    route_sources.append(stream.read())
    for endpoint in CONTRACT["endpoints"]:
        source_path = re.sub(r"(?<=/client-profiles/)(openai|xai)(?=/|$)", "{provider}", endpoint["path"])
        if not any(source_path.encode() in data for data in route_sources):
            raise RuntimeError("CPR management route changed: " + endpoint["path"])
    build_root = cache / "build"
    build_root.mkdir(exist_ok=True)
    tracked = {}
    archive = git(source, "archive", "--format=tar", REVISION, "frontend")
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
        for member in tar:
            if not member.isfile():
                if member.isdir():
                    continue
                raise RuntimeError("Unsupported CPR frontend archive entry")
            target = safe_target(build_root, member.name)
            stream = tar.extractfile(member)
            if stream is None:
                raise RuntimeError("Missing CPR frontend archive content")
            tracked[member.name] = stream.read()
    for relative, expected in LOCK.get("source_file_sha256", {}).items():
        if hashlib.sha256(tracked["frontend/" + relative].replace(b"\r\n", b"\n")).hexdigest() != expected:
            raise RuntimeError("CPR dependency baseline changed; review the security overrides")
    for path in sorted((ROOT / "overrides").rglob("*")):
        if path.is_file():
            relative = "frontend/" + path.relative_to(ROOT / "overrides").as_posix()
            tracked[relative] = path.read_bytes()
    # 用户收费、安全设置与备份入口由宿主提供，控制台只开放账号池运行和上游设置。
    settings_path = "frontend/src/views/settings/index.vue"
    settings = tracked[settings_path].decode().replace("\r\n", "\n")
    for entry in ("  { label: '模型定价', value: 'pricing' },\n", "  { label: '安全与访问', value: 'access' },\n", "  { label: '数据备份', value: 'backup' },\n"):
        if settings.count(entry) != 1:
            raise RuntimeError("CPR settings navigation changed; review the host adapter")
        settings = settings.replace(entry, "")
    tracked[settings_path] = settings.encode()
    overview_path = "frontend/src/views/accounts/components/AccountOverviewCards.vue"
    overview = tracked[overview_path].decode()
    for original, replacement in (
        ("grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4", "grid-cols-2 gap-3 sm:grid-cols-4"),
        ("inline-flex size-9 shrink-0 items-center justify-center self-start rounded-lg", "hidden size-9 shrink-0 items-center justify-center self-start rounded-lg sm:inline-flex"),
    ):
        if overview.count(original) != 1:
            raise RuntimeError("CPR overview layout changed; review the responsive adapter")
        overview = overview.replace(original, replacement)
    tracked[overview_path] = overview.encode()
    # 固定页面使用的 after-leave 不是其固定 UI 包公开事件，closed 才会清理凭据草稿。
    accounts_path = "frontend/src/views/accounts/index.vue"
    accounts = tracked[accounts_path].decode()
    if accounts.count("@after-leave=") != 3:
        raise RuntimeError("CPR account dialog events changed; review credential cleanup")
    accounts = accounts.replace("@after-leave=", "@closed=")
    account_layout = "flex min-h-0 w-full flex-col xl:h-full xl:overflow-hidden"
    if accounts.count(account_layout) != 1:
        raise RuntimeError("CPR account page scrolling changed")
    accounts = accounts.replace(account_layout, "flex min-h-0 w-full flex-col")
    vue_import = "import { ref, shallowRef } from 'vue'"
    columns = "const { visibleColumns, columnOptions, setColumnVisible, setColumnOrder, resetColumns } = useTableColumns(accountColumns, 'accounts')"
    if accounts.count(vue_import) != 1 or accounts.count(columns) != 1 or accounts.count(':columns="visibleColumns"') != 1:
        raise RuntimeError("CPR account table changed; review compact actions")
    accounts = accounts.replace(vue_import, "import { computed, ref, shallowRef } from 'vue'\nimport { useMediaQuery } from '@vueuse/core'")
    accounts = accounts.replace(columns, columns + """
const compactActions = useMediaQuery('(max-width: 639px)')
const responsiveColumns = computed(() => visibleColumns.value.map(column =>
  compactActions.value && column.key === 'actions'
    ? { ...column, size: 'sm' as const, width: 44, minWidth: 44 }
    : column,
))
""")
    tracked[accounts_path] = accounts.replace(':columns="visibleColumns"', ':columns="responsiveColumns"').encode()
    actions_path = "frontend/src/views/accounts/components/AccountTableActions.vue"
    actions = tracked[actions_path].decode().replace("\r\n", "\n")
    for label in ("编辑账号", "删除账号"):
        marker = 'aria-label="' + label + '"'
        if actions.count(marker) != 1:
            raise RuntimeError("CPR row actions changed; review the mobile menu")
    # 显示规则放在包装元素，避免 UI 组件的默认 display utility 覆盖隐藏状态。
    action_start = '    <ZIconButton\n      size="small"\n      aria-label="编辑账号"'
    popover_start = '    <ZPopover placement="bottom-end">'
    if actions.count(action_start) != 1 or actions.count(popover_start) != 1:
        raise RuntimeError("CPR inline action structure changed")
    actions = actions.replace(action_start, '    <span class="hidden sm:contents">\n' + action_start)
    actions = actions.replace(popover_start, '    </span>\n' + popover_start)
    menu = '<div class="w-40 p-1.5">'
    if actions.count(menu) != 1:
        raise RuntimeError("CPR row action menu changed")
    actions = actions.replace(menu, menu + """
          <div class="sm:hidden">
          <ZMenuItem @click.stop="(close(), emit('edit', account))">
            <template #icon><Pencil class="size-3.5 text-cp-text-quaternary" /></template>
            编辑账号
          </ZMenuItem>
          <ZMenuItem :disabled="deleting" @click.stop="(close(), emit('delete', account))">
            <template #icon><Trash2 class="size-3.5 text-cp-error" /></template>
            删除账号
          </ZMenuItem>
          </div>
""")
    tracked[actions_path] = actions.encode()
    package_path = "frontend/package.json"
    package = json.loads(tracked[package_path])
    package["scripts"]["dev"] = "vite --host 127.0.0.1"
    tracked[package_path] = (json.dumps(package, ensure_ascii=False, indent=2) + "\n").encode()
    tracked["frontend/public/CPR-LICENSE.txt"] = git(source, "show", REVISION + ":LICENSE")
    tracked["frontend/public/CPR-NOTICE.txt"] = (
        "CPR account console source: " + LOCK["repository"] + "\nCommit: " + REVISION
        + "\nSub2API modifies authentication, transport, navigation, layout and packaging for its account-pool module.\n"
    ).encode()
    manifest = build_root / "source-manifest.json"
    previous = json.loads(manifest.read_text(encoding="utf-8")) if manifest.exists() else {}
    for relative in previous.get("files", []):
        if relative not in tracked:
            target = safe_target(build_root, relative)
            if target.is_file():
                target.unlink()
    for relative, data in tracked.items():
        write(safe_target(build_root, relative), data)
    signature = hashlib.sha256()
    for relative, data in sorted(tracked.items()):
        signature.update(relative.encode() + b"\0" + data)
    write(manifest, (json.dumps({"commit": REVISION, "signature": signature.hexdigest(), "files": sorted(tracked)}, indent=2) + "\n").encode())
    print("Prepared CPR account console from " + REVISION[:12])


if __name__ == "__main__":
    main()
