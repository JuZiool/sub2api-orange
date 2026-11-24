#requires -Version 7.3
# Orange 特有：无 Docker/WSLC 服务依赖的部署脚本测试。
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '../wslc-common.ps1')
$script:passed = 0
function Assert-Equal($Actual,$Expected,[string]$Name) {
    if ($Actual -cne $Expected) { throw "FAIL: $Name" }
    $script:passed++
}
function Assert-Throws([scriptblock]$Work,[string]$Name) {
    $thrown = $false
    try { & $Work | Out-Null } catch { $thrown = $true }
    if (-not $thrown) { throw "FAIL: $Name 应当拒绝输入" }
    $script:passed++
}
$tempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$temp = Join-Path $tempRoot ('orange-wslc-tests-' + [guid]::NewGuid().ToString('N'))
[System.IO.Directory]::CreateDirectory($temp) | Out-Null
try {
    $envPath = Join-Path $temp 'test.env'
    [System.IO.File]::WriteAllLines($envPath,[string[]]@(
        '# comment', 'A=plain', 'B="space # literal" # comment', "C='single # literal'",
        'D=value # trailing comment', 'EMPTY=', 'export E=exported', 'LITERAL=$(throw "no eval")'
    ),[System.Text.UTF8Encoding]::new($false))
    $values = Read-OrangeEnv -Path $envPath
    Assert-Equal $values.A 'plain' '普通 env'
    Assert-Equal $values.B 'space # literal' '双引号与注释'
    Assert-Equal $values.C 'single # literal' '单引号'
    Assert-Equal $values.D 'value' '行尾注释'
    Assert-Equal $values.EMPTY '' '空值'
    Assert-Equal $values.E 'exported' 'export'
    Assert-Equal $values.LITERAL '$(throw "no eval")' '不能执行 env 表达式'
    Assert-Equal (Resolve-OrangeComposeValue '${MISSING:-fallback}' $values) 'fallback' '默认值插值'
    Assert-Equal (Resolve-OrangeComposeValue '${A:-fallback}' $values) 'plain' '现有值插值'
    Assert-Equal (Resolve-OrangeComposeValue '${EMPTY:-fallback}' $values) 'fallback' '空值默认'
    Assert-Equal (Resolve-OrangeComposeValue '${A:+yes}' $values) 'yes' '条件插值'
    Assert-Throws { Resolve-OrangeComposeValue '${MISSING:?required}' $values } '必填变量'
    [System.IO.File]::WriteAllText($envPath,'A="unterminated',[System.Text.UTF8Encoding]::new($false))
    Assert-Throws { Read-OrangeEnv $envPath } '未闭合引用'
    $app = Get-OrangeAppEnvironment -ComposePath (Join-Path $PSScriptRoot '../docker-compose.local.yml') -Environment @{
        POSTGRES_PASSWORD='test-password'; JWT_SECRET='existing-jwt'; TOTP_ENCRYPTION_KEY=''; SERVER_PORT='9000'
    }
    Assert-Equal $app.DATABASE_PASSWORD 'test-password' '复用数据库密码'
    Assert-Equal $app.JWT_SECRET 'existing-jwt' '保留 JWT'
    Assert-Equal $app.ContainsKey('TOTP_ENCRYPTION_KEY') $false '空密钥不覆盖旧配置'
    Assert-Equal $app.AUTO_SETUP 'false' '禁止重新初始化'
    Assert-Equal $app.ADMIN_EMAIL '' '缺省邮箱交由后端生成，不使用固定默认值'
    $configuredAdmin = Get-OrangeAppEnvironment -ComposePath (Join-Path $PSScriptRoot '../docker-compose.local.yml') -Environment @{
        POSTGRES_PASSWORD='test-password'; ADMIN_EMAIL='existing@example.com'; ADMIN_PASSWORD='existing-password'
    }
    Assert-Equal $configuredAdmin.ADMIN_EMAIL 'existing@example.com' '保留显式管理员邮箱'
    Assert-Equal $configuredAdmin.ADMIN_PASSWORD 'existing-password' '保留显式管理员密码'
    Assert-Equal $configuredAdmin.AUTO_SETUP 'false' '有管理员配置时仍禁止重新初始化'
    Assert-Equal $app.SERVER_PORT '8080' '固定容器端口'
    Assert-Equal $app.DATABASE_HOST 'postgres' '数据库别名'
    Assert-Equal $app.REDIS_HOST 'redis' 'Redis 别名'
    Assert-Throws { Get-OrangeAppEnvironment (Join-Path $PSScriptRoot '../docker-compose.local.yml') @{} } '无数据库密码不能部署'
    $redisWithoutPassword = Get-OrangeRedisEnvironment -AppEnvironment @{REDIS_PASSWORD='';TZ='Asia/Shanghai'}
    Assert-Equal $redisWithoutPassword.ContainsKey('REDISCLI_AUTH') $false '无密码时禁止 CLI 空认证'
    $redisWithPassword = Get-OrangeRedisEnvironment -AppEnvironment @{REDIS_PASSWORD='existing-redis';TZ='Asia/Shanghai'}
    Assert-Equal $redisWithPassword.REDISCLI_AUTH 'existing-redis' 'Redis 保留现有认证密码'
    $file = Join-Path $temp 'generated.env'
    Write-OrangeEnvironmentFile -Path $file -Values @{Z='a$b';A='space # value'}
    $bytes = [System.IO.File]::ReadAllBytes($file)
    Assert-Equal ($bytes[0] -eq 0xef) $false 'env 无 UTF8 BOM'
    Assert-Equal ([System.IO.File]::ReadAllLines($file)[1]) 'Z=a$b' 'env 不执行变量'
    Assert-Throws { Write-OrangeEnvironmentFile $file @{KEY="bad`nINJECT=1"} } '拒绝多行 env 注入'
    $data = Join-Path $temp '中文 数据'
    [System.IO.Directory]::CreateDirectory($data) | Out-Null
    Assert-Equal (Get-OrangeBindMount $data '/app/data') "type=bind,source=$data,target=/app/data" '中文及空格路径'
    Assert-Throws { Get-OrangeBindMount (Join-Path $temp 'absent') '/app/data' } '禁止创建空数据目录'
    $owned = [pscustomobject]@{Config=[pscustomobject]@{Labels=[pscustomobject]@{'org.sub2api.orange.stack'='wslc-local'}}}
    Assert-OrangeWslcOwner $owned container 'test'; $script:passed++
    $foreign = [pscustomobject]@{Config=[pscustomobject]@{Labels=$null}}
    Assert-Throws { Assert-OrangeWslcOwner $foreign container 'foreign' } '不得接管其他容器'
    Assert-Equal (Get-OrangeHash 'same') (Get-OrangeHash 'same') '稳定配置指纹'
    $script:fake = [pscustomobject]@{ExitCode=0;Text='[{"Id":"test-id"}]'}
    function Invoke-OrangeWslc { param([string[]]$Arguments,[switch]$AllowFailure); return $script:fake }
    Assert-Equal (Get-OrangeWslcObject image 'test').Id 'test-id' '解析 inspect 数组'
    $script:fake = [pscustomobject]@{ExitCode=1;Text='not found'}
    Assert-Equal ($null -eq (Get-OrangeWslcObject container 'absent')) $true '不存在资源'
    # 所有生产脚本语法检查，只解析，不执行。
    foreach ($name in @('wslc-common.ps1','wslc-build.ps1','wslc-deploy.ps1')) {
        $tokens=$null; $errors=$null
        [System.Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot "../$name"),[ref]$tokens,[ref]$errors) | Out-Null
        Assert-Equal $errors.Count 0 "$name 语法检查"
    }
    Write-Host "PASS: $script:passed 项 WSLC 部署测试（未启动任何容器）。"
} finally {
    $resolved=[System.IO.Path]::GetFullPath($temp)
    $allowedRoot=$tempRoot.TrimEnd('\','/') + [System.IO.Path]::DirectorySeparatorChar
    if (-not $resolved.StartsWith($allowedRoot,[StringComparison]::OrdinalIgnoreCase) -or
        [System.IO.Path]::GetFileName($resolved) -notmatch '^orange-wslc-tests-[0-9a-f]{32}$') { throw '测试临时路径不安全，拒绝删除。' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}