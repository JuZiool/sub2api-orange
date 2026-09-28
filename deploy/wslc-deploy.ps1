#requires -Version 7.3
# Orange 特有：WSLC 本地部署，复用现有测试数据，固定主机端口 8080。
[CmdletBinding()]
param(
    [ValidateSet('up','down','restart','status','logs','health')][string]$Action = 'up',
    [ValidateSet('app','postgres','redis')][string]$Service = 'app',
    [string]$Image,
    [switch]$Build,
    [switch]$Follow,
    [ValidateRange(10,1800)][int]$TimeoutSeconds = 180
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'wslc-common.ps1')
if (-not $IsWindows) { throw '本脚本用于 Windows 上的 WSLC。' }
Get-Command wslc -ErrorAction Stop | Out-Null
$null = Invoke-OrangeWslc -Arguments @('info')
$deployDir = $PSScriptRoot
$stateDir = Join-Path $deployDir '.wslc'
$names = @{app='orange-wslc-app'; postgres='orange-wslc-postgres'; redis='orange-wslc-redis'}
$network = 'orange-wslc-network'
$mutex = $null; $locked = $false
try {
    if ($Action -in @('up','restart','down')) {
        $mutex = [System.Threading.Mutex]::new($false,'Local\OrangeWslcDeploy')
        $locked = $mutex.WaitOne(0)
        if (-not $locked) { throw '已有 Orange WSLC 部署操作正在进行。' }
    }
    switch ($Action) {
        'status' {
            foreach ($serviceName in @('app','postgres','redis')) {
                $object = Get-OrangeWslcObject -Type container -Name $names[$serviceName]
                Assert-OrangeWslcOwner -Object $object -Type container -Name $names[$serviceName]
                if ($null -eq $object) { Write-Host "$serviceName : 未创建" }
                else { Write-Host "$serviceName : $($object.State.Status) / $($object.Config.Image)" }
            }
            return
        }
        'logs' {
            $object = Get-OrangeWslcObject -Type container -Name $names[$Service]
            if ($null -eq $object) { throw '指定服务尚未创建。' }
            Assert-OrangeWslcOwner -Object $object -Type container -Name $names[$Service]
            $arguments = @('logs','--tail','200')
            if ($Follow) { $arguments += '--follow' }
            & wslc @arguments $names[$Service]
            if ($LASTEXITCODE -ne 0) { throw '读取容器日志失败。' }
            return
        }
        'down' {
            foreach ($serviceName in @('app','redis','postgres')) { Stop-OrangeContainer -Name $names[$serviceName] }
            Write-Host '已停止自有容器，所有旧数据和容器均保留。'
            return
        }
        'health' {
            foreach ($serviceName in @('app','postgres','redis')) {
                $object = Get-OrangeWslcObject -Type container -Name $names[$serviceName]
                if ($null -eq $object) { throw "$serviceName 尚未创建。" }
                Assert-OrangeWslcOwner -Object $object -Type container -Name $names[$serviceName]
            }
            $envValues = Read-OrangeEnv -Path (Join-Path $deployDir '.env')
            $user = if ($envValues.ContainsKey('POSTGRES_USER') -and $envValues.POSTGRES_USER) { $envValues.POSTGRES_USER } else { 'sub2api' }
            $database = if ($envValues.ContainsKey('POSTGRES_DB') -and $envValues.POSTGRES_DB) { $envValues.POSTGRES_DB } else { 'sub2api' }
            Wait-OrangeDependency -Name $names.postgres -Probe @('pg_isready','-U',$user,'-d',$database) -TimeoutSeconds $TimeoutSeconds
            Wait-OrangeDependency -Name $names.redis -Probe @('redis-cli','ping') -TimeoutSeconds $TimeoutSeconds
            $null = Wait-OrangeHealth -TimeoutSeconds $TimeoutSeconds
            Write-Host 'PostgreSQL、Redis 与 Orange /health 均通过检查：http://127.0.0.1:8080'
            return
        }
    }
    if ($Build) { & (Join-Path $deployDir 'wslc-build.ps1') }
    $metadataPath = Join-Path $stateDir 'build.json'
    $metadata = $null
    if (Test-Path -LiteralPath $metadataPath) { $metadata = [System.IO.File]::ReadAllText($metadataPath) | ConvertFrom-Json }
    if (-not $Image) {
        if ($null -eq $metadata) { throw '没有本地构建记录，请先运行 wslc-build.ps1 或指定 -Build。' }
        $Image = $metadata.image
    }
    if ($Image -notmatch '^sub2api-orange:wslc-[a-zA-Z0-9_.-]+$') { throw '只允许部署 sub2api-orange:wslc- 前缀的本地构建镜像。' }
    if ($null -eq (Get-OrangeWslcObject -Type image -Name $Image)) { throw '本地构建镜像不存在。' }
    foreach ($relative in @('.env','data/config.yaml','data/.installed','postgres_data/PG_VERSION')) {
        if (-not (Test-Path -LiteralPath (Join-Path $deployDir $relative) -PathType Leaf)) { throw "缺少现有配置/数据 $relative，不进行全新初始化。" }
    }
    if ([System.IO.File]::ReadAllText((Join-Path $deployDir 'postgres_data/PG_VERSION')).Trim() -ne '18') {
        throw '现有 PostgreSQL 数据不是版本 18，拒绝启动不匹配的数据库镜像。'
    }
    $envValues = Read-OrangeEnv -Path (Join-Path $deployDir '.env')
    $appEnv = Get-OrangeAppEnvironment -ComposePath (Join-Path $deployDir 'docker-compose.local.yml') -Environment $envValues
    $pgEnv = @{
        POSTGRES_USER=$appEnv.DATABASE_USER; POSTGRES_PASSWORD=$appEnv.DATABASE_PASSWORD
        POSTGRES_DB=$appEnv.DATABASE_DBNAME; PGDATA='/var/lib/postgresql/data'; TZ=$appEnv.TZ
    }
    $redisEnv = Get-OrangeRedisEnvironment -AppEnvironment $appEnv
    $appMount = Get-OrangeBindMount -Source (Join-Path $deployDir 'data') -Target '/app/data'
    $pgMount = Get-OrangeBindMount -Source (Join-Path $deployDir 'postgres_data') -Target '/var/lib/postgresql/data'
    $redisMount = Get-OrangeBindMount -Source (Join-Path $deployDir 'redis_data') -Target '/data'
    # 在修改任何资源前先检查所有资源归属。
    foreach ($serviceName in @('app','postgres','redis')) {
        $object = Get-OrangeWslcObject -Type container -Name $names[$serviceName]
        Assert-OrangeWslcOwner -Object $object -Type container -Name $names[$serviceName]
    }
    $netObject = Get-OrangeWslcObject -Type network -Name $network
    Assert-OrangeWslcOwner -Object $netObject -Type network -Name $network
    foreach ($serviceName in @('app','redis','postgres')) { Stop-OrangeContainer -Name $names[$serviceName] }
    Assert-OrangePort
    Initialize-OrangeStateDirectory -Path $stateDir
    Get-Command tar -ErrorAction Stop | Out-Null
    $backup = Backup-OrangeExistingData -DeployDirectory $deployDir -StateDirectory $stateDir
    $pgFile = Join-Path $stateDir 'postgres.env'; $redisFile = Join-Path $stateDir 'redis.env'; $appFile = Join-Path $stateDir 'app.env'
    Write-OrangeEnvironmentFile -Path $pgFile -Values $pgEnv
    Write-OrangeEnvironmentFile -Path $redisFile -Values $redisEnv
    Write-OrangeEnvironmentFile -Path $appFile -Values $appEnv
    if ($null -eq $netObject) {
        $null = Invoke-OrangeWslc -Arguments @('network','create','--label','org.sub2api.orange.stack=wslc-local',$network)
    }
    Ensure-OrangeContainer -Name $names.postgres -EnvFile $pgFile -RunArguments @(
        '--network',$network,'--network-alias','postgres','--mount',$pgMount,'postgres:18-alpine')
    Wait-OrangeDependency -Name $names.postgres -Probe @('pg_isready','-U',$pgEnv.POSTGRES_USER,'-d',$pgEnv.POSTGRES_DB) -TimeoutSeconds $TimeoutSeconds
    Ensure-OrangeContainer -Name $names.redis -EnvFile $redisFile -RunArguments @(
        '--network',$network,'--network-alias','redis','--mount',$redisMount,'redis:8-alpine',
        'sh','-c','exec redis-server --save 60 1 --appendonly yes --appendfsync everysec ${REDIS_PASSWORD:+--requirepass "$REDIS_PASSWORD"}')
    Wait-OrangeDependency -Name $names.redis -Probe @('redis-cli','ping') -TimeoutSeconds $TimeoutSeconds
    Ensure-OrangeContainer -Name $names.app -EnvFile $appFile -RunArguments @(
        '--network',$network,'--publish','127.0.0.1:8080:8080','--mount',$appMount,'--pull','never',$Image)
    $null = Wait-OrangeHealth -TimeoutSeconds $TimeoutSeconds
    Write-OrangeJson -Path (Join-Path $stateDir 'deploy.json') -Value ([ordered]@{
        image=$Image; healthyAt=[DateTime]::UtcNow.ToString('o'); backup=$backup
        dataDirectory=$deployDir; dataMode='existing-bind'; port=8080
    })
    Write-Host 'WSLC 部署成功：http://127.0.0.1:8080；复用旧测试数据和账号。'
} finally {
    if ($locked) { $mutex.ReleaseMutex() }
    if ($null -ne $mutex) { $mutex.Dispose() }
}