#requires -Version 7.3
# Orange 特有：仅从本地已提交源码构建 WSLC 镜像。
[CmdletBinding()]
param(
    [string]$Image,
    [string[]]$BuildArgument = @(),
    [switch]$NoCache
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'wslc-common.ps1')
$repo = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$stateDir = Join-Path $PSScriptRoot '.wslc'
if (-not $IsWindows) { throw '本脚本用于 Windows 上的 WSLC。' }
foreach ($command in @('wslc','git','tar')) { Get-Command $command -ErrorAction Stop | Out-Null }
$null = Invoke-OrangeWslc -Arguments @('info')
$dirty = @(& git -C $repo status --porcelain --untracked-files=no)
if ($LASTEXITCODE -ne 0) { throw '无法检查本地 Git 状态。' }
if ($dirty.Count) { throw '存在未提交的跟踪文件改动。请先中文提交并标注来源，再构建部署。' }
$commit = (& git -C $repo rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0) { throw '无法读取本地源码提交。' }
$short = $commit.Substring(0,12)
if (-not $Image) { $Image = "sub2api-orange:wslc-$short" }
if ($Image -notmatch '^sub2api-orange:wslc-[a-zA-Z0-9_.-]+$') { throw '镜像标记须使用 sub2api-orange:wslc- 前缀。' }
$version = [System.IO.File]::ReadAllText((Join-Path $repo 'backend/cmd/server/VERSION')).Trim()
Initialize-OrangeStateDirectory -Path $stateDir
$mutexName = 'Local\OrangeWslcBuild-' + (Get-OrangeHash -Text $repo).Substring(0,20)
$mutex = [System.Threading.Mutex]::new($false,$mutexName)
$locked = $false
$temp = $null
try {
    $locked = $mutex.WaitOne(0)
    if (-not $locked) { throw '已有 WSLC 构建正在进行。' }
    # ASCII 临时目录避免中文路径兼容问题；git archive 排除未跟踪文件、业务数据和密钥。
    $tempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    $temp = Join-Path $tempRoot ('orange-wslc-build-' + [guid]::NewGuid().ToString('N'))
    [System.IO.Directory]::CreateDirectory($temp) | Out-Null
    $source = Join-Path $temp 'source'
    [System.IO.Directory]::CreateDirectory($source) | Out-Null
    $archive = Join-Path $temp 'source.tar'
    & git -C $repo archive --format=tar "--output=$archive" $commit
    if ($LASTEXITCODE -ne 0) { throw '导出本地已提交源码失败。' }
    & tar -xf $archive -C $source
    if ($LASTEXITCODE -ne 0) { throw '展开本地源码快照失败。' }
    $arguments = @('build','--progress','plain','--tag',$Image,
        '--build-arg',"VERSION=$version",'--build-arg',"COMMIT=$short",
        '--build-arg',('DATE=' + [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')))
    if ($NoCache) { $arguments += '--no-cache' }
    foreach ($argument in $BuildArgument) {
        if ($argument -notmatch '^[A-Za-z_][A-Za-z0-9_]*=') { throw '构建参数须为 KEY=VALUE。' }
        $arguments += @('--build-arg',$argument)
    }
    $arguments += $source
    Write-Host "从本地提交 $short 构建 Orange $version：$Image"
    & wslc @arguments
    if ($LASTEXITCODE -ne 0) { throw 'WSLC 本地源码构建失败，未更新构建状态。' }
    $built = Get-OrangeWslcObject -Type image -Name $Image
    if ($null -eq $built) { throw '构建命令完成但镜像不可用。' }
    Write-OrangeJson -Path (Join-Path $stateDir 'build.json') -Value ([ordered]@{
        image=$Image; imageId=$built.Id; commit=$commit; version=$version
        builtAt=[DateTime]::UtcNow.ToString('o'); source='local-git-archive'
    })
    Write-Host "构建完成：$Image"
} finally {
    if ($null -ne $temp -and (Test-Path -LiteralPath $temp)) {
        $resolved = [System.IO.Path]::GetFullPath($temp)
        $allowedRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\','/') + [System.IO.Path]::DirectorySeparatorChar
        if (-not $resolved.StartsWith($allowedRoot,[StringComparison]::OrdinalIgnoreCase) -or
            [System.IO.Path]::GetFileName($resolved) -notmatch '^orange-wslc-build-[0-9a-f]{32}$') {
            throw '构建临时路径校验失败，保留文件以免误删。'
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
    if ($locked) { $mutex.ReleaseMutex() }
    $mutex.Dispose()
}