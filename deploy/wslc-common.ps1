#requires -Version 7.3
# Orange 特有：WSLC 本地构建部署公共函数，不依赖 Docker Desktop。
Set-StrictMode -Version Latest

function Invoke-OrangeWslc {
    param([Parameter(Mandatory)][string[]]$Arguments, [switch]$AllowFailure)
    $output = @(& wslc @Arguments 2>&1)
    $code = $LASTEXITCODE
    $text = ($output | ForEach-Object { $_.ToString() }) -join "`n"
    if ($code -ne 0 -and -not $AllowFailure) {
        throw "WSLC 命令失败（$($Arguments[0])，退出码 $code）：$text"
    }
    return [pscustomobject]@{ ExitCode = $code; Text = $text }
}

function Get-OrangeWslcObject {
    param([ValidateSet('container','image','network')][string]$Type, [string]$Name)
    $result = Invoke-OrangeWslc -Arguments @('inspect','--type',$Type,$Name) -AllowFailure
    if ($result.ExitCode -ne 0) { return $null }
    $items = @($result.Text | ConvertFrom-Json -Depth 64)
    if ($items.Count -ne 1) { throw "无法确定 WSLC 对象：$Type/$Name" }
    return $items[0]
}

function Assert-OrangeWslcOwner {
    param($Object, [string]$Type, [string]$Name)
    if ($null -eq $Object) { return }
    $labels = if ($Type -eq 'container') { $Object.Config.Labels } else { $Object.Labels }
    if ($null -eq $labels -or $null -eq $labels.PSObject.Properties['org.sub2api.orange.stack'] -or
        $labels.'org.sub2api.orange.stack' -ne 'wslc-local') {
        throw "资源 $Name 不属于 Orange WSLC，拒绝接管或停止。"
    }
}

function Read-OrangeEnv {
    param([Parameter(Mandatory)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "缺少现有环境文件：$Path" }
    $values = @{}
    $lineNumber = 0
    foreach ($line in [System.IO.File]::ReadAllLines($Path)) {
        $lineNumber++
        $text = $line.Trim()
        if (-not $text -or $text.StartsWith('#')) { continue }
        if ($text -notmatch '^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=(.*)$') {
            throw "环境文件第 $lineNumber 行不是受支持的 KEY=VALUE 格式。"
        }
        $key = $Matches[1]
        $value = $Matches[2].Trim()
        if ($value.StartsWith('"') -or $value.StartsWith("'")) {
            $quote = $value[0]
            # 支持整行引用及引用后的注释，不执行表达式或命令。
            $pattern = if ($quote -eq '"') { '^"((?:\\.|[^"\\])*)"\s*(?:#.*)?$' } else { "^'([^']*)'\s*(?:#.*)?$" }
            if ($value -notmatch $pattern) { throw "环境文件第 $lineNumber 行引用不完整。" }
            $value = $Matches[1]
            if ($quote -eq '"') {
                $value = $value.Replace('\"','"').Replace('\\','\').Replace('\$','$')
            }
        } else { $value = $value -replace '\s+#.*$','' }
        if ($value -match '[\r\n\x00]') { throw "环境变量 $key 含不受支持的控制字符。" }
        $values[$key] = $value
    }
    return $values
}

function Resolve-OrangeComposeValue {
    param([string]$Value, [hashtable]$Environment)
    return [regex]::Replace($Value, '\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:-|:\?|:\+)([^{}]*))?\}', {
        param($match)
        $key = $match.Groups[1].Value
        $operation = $match.Groups[2].Value
        $fallback = $match.Groups[3].Value
        $set = $Environment.ContainsKey($key) -and -not [string]::IsNullOrEmpty([string]$Environment[$key])
        if ($set) {
            if ($operation -eq ':+') { return $fallback }
            return [string]$Environment[$key]
        }
        if ($operation -eq ':?') { throw "环境变量 $key 必须在现有 .env 中配置。" }
        if ($operation -eq ':-') { return $fallback }
        return ''
    }.GetNewClosure())
}

function Get-OrangeAppEnvironment {
    param([string]$ComposePath, [hashtable]$Environment)
    $app = @{}
    $inService = $false
    $inEnvironment = $false
    foreach ($line in [System.IO.File]::ReadAllLines($ComposePath)) {
        if ($line -match '^  ([\w-]+):\s*$') { $inService = $Matches[1] -eq 'sub2api'; $inEnvironment = $false }
        if (-not $inService) { continue }
        if ($line -match '^    environment:\s*$') { $inEnvironment = $true; continue }
        if ($line -match '^    [\w-]+:') { $inEnvironment = $false }
        if ($inEnvironment -and $line -match '^      - ([A-Za-z_][A-Za-z0-9_]*)=(.*)$') {
            $key = $Matches[1]; $value = $Matches[2]
            $app[$key] = Resolve-OrangeComposeValue -Value $value -Environment $Environment
        }
    }
    if (-not $app.ContainsKey('DATABASE_HOST')) { throw '未找到 Compose 中的应用环境配置，拒绝猜测配置。' }
    # 复用已安装数据，不进入自动初始化或重置管理员流程。
    $app['AUTO_SETUP'] = 'false'
    $app['SERVER_HOST'] = '0.0.0.0'
    $app['SERVER_PORT'] = '8080'
    $app['DATABASE_HOST'] = 'postgres'
    $app['REDIS_HOST'] = 'redis'
    # 不以空值覆盖现有 config.yaml 中的密钥。
    foreach ($key in @('JWT_SECRET','TOTP_ENCRYPTION_KEY')) {
        if ([string]::IsNullOrEmpty([string]$app[$key])) { $app.Remove($key) }
    }
    return $app
}

function Write-OrangeEnvironmentFile {
    param([string]$Path, [hashtable]$Values)
    $lines = foreach ($key in @($Values.Keys | Sort-Object)) {
        $value = [string]$Values[$key]
        if ($key -notmatch '^[A-Za-z_][A-Za-z0-9_]*$' -or $value -match '[\r\n\x00]') {
            throw '环境变量格式非法，拒绝写入。'
        }
        "$key=$value"
    }
    [System.IO.File]::WriteAllLines($Path, [string[]]$lines, [System.Text.UTF8Encoding]::new($false))
}

function Get-OrangeBindMount {
    param([string]$Source, [string]$Target)
    $full = [System.IO.Path]::GetFullPath($Source)
    if (-not (Test-Path -LiteralPath $full -PathType Container)) { throw "现有数据目录不存在：$full" }
    if ($full.Contains(',') -or $full.Contains('"') -or $Target.Contains(',')) { throw '挂载路径不能包含逗号或引号。' }
    return "type=bind,source=$full,target=$Target"
}

function Assert-OrangePort {
    # 不能改用其他端口；停止自有应用后再调用。
    $listeners = @(Get-NetTCPConnection -LocalPort 8080 -State Listen -ErrorAction SilentlyContinue)
    if ($listeners.Count) { throw "8080 已被占用（PID：$($listeners.OwningProcess -join ',')），请先处理占用服务。" }
    $probe = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback,8080)
    try { $probe.Start() } catch { throw '无法绑定主机 8080 端口，请检查占用或端口保留规则。' }
    finally { $probe.Stop() }
}

function Initialize-OrangeStateDirectory {
    param([string]$Path)
    [System.IO.Directory]::CreateDirectory($Path) | Out-Null
    if ($IsWindows) {
        $sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        & icacls $Path /inheritance:r /grant:r "*${sid}:(OI)(CI)F" '*S-1-5-18:(OI)(CI)F' | Out-Null
        if ($LASTEXITCODE -ne 0) { throw '无法保护 WSLC 环境文件目录权限，停止写入密钥。' }
    }
}

function Write-OrangeJson {
    param([string]$Path, $Value)
    $temporary = "$Path.tmp"
    [System.IO.File]::WriteAllText($temporary, ($Value | ConvertTo-Json -Depth 12), [System.Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $temporary -Destination $Path -Force
}

function Get-OrangeHash {
    param([string]$Text)
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($Text)
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Stop-OrangeContainer {
    param([string]$Name)
    $object = Get-OrangeWslcObject -Type container -Name $Name
    Assert-OrangeWslcOwner -Object $object -Type container -Name $Name
    if ($null -ne $object -and $object.State.Running) {
        $null = Invoke-OrangeWslc -Arguments @('stop',$Name)
    }
}

function Ensure-OrangeContainer {
    param([string]$Name, [string[]]$RunArguments, [string]$EnvFile)
    $hash = Get-OrangeHash -Text (($RunArguments -join "`n") + "`n" + [System.IO.File]::ReadAllText($EnvFile))
    $object = Get-OrangeWslcObject -Type container -Name $Name
    Assert-OrangeWslcOwner -Object $object -Type container -Name $Name
    if ($null -ne $object) {
        $label = $object.Config.Labels.PSObject.Properties['org.sub2api.orange.config']
        if ($null -ne $label -and $label.Value -eq $hash) {
            if (-not $object.State.Running) { $null = Invoke-OrangeWslc -Arguments @('start',$Name) }
            return
        }
        Stop-OrangeContainer -Name $Name
        # 不使用 --volumes，不删除任何持久化数据。
        $null = Invoke-OrangeWslc -Arguments @('remove',$Name)
    }
    $arguments = @('run','--detach','--name',$Name,'--label','org.sub2api.orange.stack=wslc-local',
        '--label',"org.sub2api.orange.config=$hash",'--env-file',$EnvFile) + $RunArguments
    $null = Invoke-OrangeWslc -Arguments $arguments
}

function Wait-OrangeDependency {
    param([string]$Name, [string[]]$Probe, [int]$TimeoutSeconds = 180)
    $timer = [System.Diagnostics.Stopwatch]::StartNew()
    while ($timer.Elapsed.TotalSeconds -lt $TimeoutSeconds) {
        $result = Invoke-OrangeWslc -Arguments (@('exec',$Name) + $Probe) -AllowFailure
        if ($result.ExitCode -eq 0) { return }
        $object = Get-OrangeWslcObject -Type container -Name $Name
        if ($null -ne $object -and -not $object.State.Running) { throw "容器 $Name 已退出，请使用 logs 查看日志。" }
        Start-Sleep -Seconds 2
    }
    throw "等待 $Name 就绪超时。"
}

function Wait-OrangeHealth {
    param([int]$TimeoutSeconds = 180)
    $timer = [System.Diagnostics.Stopwatch]::StartNew()
    while ($timer.Elapsed.TotalSeconds -lt $TimeoutSeconds) {
        try {
            $health = Invoke-RestMethod -Uri 'http://127.0.0.1:8080/health' -TimeoutSec 5
            if ($health.status -eq 'ok') { return $health }
        } catch { }
        Start-Sleep -Seconds 2
    }
    throw 'Orange 未在主机 8080 端口通过健康检查。'
}

function Backup-OrangeExistingData {
    param([string]$DeployDirectory, [string]$StateDirectory)
    $pidFile = Join-Path $DeployDirectory 'postgres_data/postmaster.pid'
    if (Test-Path -LiteralPath $pidFile) { throw '数据库仍存在 postmaster.pid，拒绝进行不一致的文件备份，请先确认数据库已停机。' }
    $backupDir = Join-Path $StateDirectory ('backups/' + (Get-Date -Format 'yyyyMMdd-HHmmss-fff'))
    [System.IO.Directory]::CreateDirectory($backupDir) | Out-Null
    $archive = Join-Path $backupDir 'existing-data.tar'
    & tar -cf $archive -C $DeployDirectory data postgres_data redis_data .env
    if ($LASTEXITCODE -ne 0) { throw '旧数据备份失败，未启动容器。' }
    Write-Host "已有测试数据已备份：$archive"
    return $archive
}