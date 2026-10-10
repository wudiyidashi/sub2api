param([string]$Source = '')
$ErrorActionPreference = 'Stop'
$driverRoot = $PSScriptRoot
$lock = Get-Content -LiteralPath (Join-Path $driverRoot 'upstream-lock.json') -Raw | ConvertFrom-Json
if (-not $Source) { $Source = $lock.repository }
$upstreamPath = Join-Path $driverRoot 'upstream'
if (-not (Test-Path -LiteralPath (Join-Path $upstreamPath '.git'))) {
    & git clone --no-checkout -- $Source $upstreamPath
    if ($LASTEXITCODE -ne 0) { throw 'CPR clone failed' }
    & git -C $upstreamPath checkout --detach $lock.commit
    if ($LASTEXITCODE -ne 0) { throw 'CPR pinned checkout failed' }
}
$actual = & git -C $upstreamPath rev-parse HEAD
if ($actual -ne $lock.commit) { throw 'Existing CPR checkout is at a different commit; preserve it and prepare a separate pinned checkout' }
$patchPath = Join-Path $driverRoot $lock.patches[0]
if ((Get-FileHash -LiteralPath $patchPath -Algorithm SHA256).Hash.ToLowerInvariant() -ne $lock.patch_sha256) { throw 'CPR patch checksum does not match upstream-lock.json' }
$existingDiff = (& git -C $upstreamPath diff --no-ext-diff) -join "`n"
$untracked = & git -C $upstreamPath ls-files --others --exclude-standard
if ($untracked) { throw 'CPR checkout contains untracked files; preserve them before preparing the dependency' }
if ($existingDiff) {
    $expectedDiff = Get-Content -LiteralPath $patchPath -Raw
    if ($existingDiff.Trim() -ne $expectedDiff.Trim()) { throw 'CPR checkout has changes beyond the maintained patch' }
    & git -C $upstreamPath apply --reverse --check $patchPath
    if ($LASTEXITCODE -ne 0) { throw 'Installed CPR patch verification failed' }
} else {
    & git -C $upstreamPath apply --check $patchPath
    if ($LASTEXITCODE -ne 0) { throw 'CPR patch does not apply to the pinned source' }
    & git -C $upstreamPath apply $patchPath
    if ($LASTEXITCODE -ne 0) { throw 'CPR patch application failed' }
}
Write-Output "CPR $actual prepared with $($lock.patches[0])"
