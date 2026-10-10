param([string]$Source = 'https://github.com/zyycn/codex-proxy-rs.git')
$ErrorActionPreference = 'Stop'
$pocRoot = $PSScriptRoot
$lock = Get-Content -LiteralPath (Join-Path $pocRoot 'upstream-lock.json') -Raw | ConvertFrom-Json
$upstreamPath = Join-Path $pocRoot 'upstream'
if (-not (Test-Path -LiteralPath (Join-Path $upstreamPath '.git'))) {
    & git clone --no-checkout -- $Source $upstreamPath
    if ($LASTEXITCODE -ne 0) { throw 'CPR clone failed' }
}
$dirty = & git -C $upstreamPath status --porcelain
if ($LASTEXITCODE -ne 0 -or $dirty) { throw 'CPR checkout has local changes; preserve them before preparing the pinned dependency' }
& git -C $upstreamPath checkout --detach $lock.commit
if ($LASTEXITCODE -ne 0) { throw 'CPR pinned checkout failed' }
$actual = & git -C $upstreamPath rev-parse HEAD
if ($actual -ne $lock.commit) { throw 'CPR commit verification failed' }
Write-Output "CPR pinned at $actual"
