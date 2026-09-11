import assert from 'node:assert/strict'
import test from 'node:test'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmodSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { buildCLIConnectCommand, buildCLIInstallCommand, JIWAI_CLI_RELEASE } from './cliIntegration'

test('公司安装指引固定源码、匹配发布补丁并使用持久二进制路径', () => {
  const patch = readFileSync(new URL('../../../public/downloads/jiwai-cli-company-2ca071fa.patch', import.meta.url))
  assert.equal(createHash('sha256').update(patch).digest('hex'), JIWAI_CLI_RELEASE.patchSHA256)
  const command = buildCLIInstallCommand()
  assert.match(command, new RegExp(`checkout --detach ${JIWAI_CLI_RELEASE.upstreamCommit}`))
  assert.ok(command.includes(JIWAI_CLI_RELEASE.patchUrl))
  assert.ok(command.includes(JIWAI_CLI_RELEASE.buildFlags))
  assert.match(command, /JIWAI_CLI_BIN_DIR="\$HOME\/\.local\/bin"/)
  assert.match(command, /weknora\.backup\.XXXXXX/)
  assert.match(command, /mv -f "\$JIWAI_CLI_NEW_BINARY" "\$JIWAI_CLI_BIN_DIR\/weknora"/)
  assert.ok(command.indexOf('"$JIWAI_CLI_NEW_BINARY" mcp serve --help') < command.indexOf('JIWAI_CLI_BACKUP='))
  assert.match(command, /"\$JIWAI_CLI_BIN_DIR\/weknora" --version &&\n"\$JIWAI_CLI_BIN_DIR\/weknora" mcp serve --help$/)
  assert.doesNotMatch(command, /export PATH="\$JIWAI_CLI_BUILD_DIR/)
  const parsed = spawnSync('/bin/sh', ['-n'], { input: command, encoding: 'utf8' })
  assert.equal(parsed.status, 0, parsed.stderr)
})

test('补丁校验失败时停止，不应用源码或编译替换已安装程序', () => {
  const fixture = mkdtempSync(join(tmpdir(), 'jiwai-cli-install-test-'))
  const log = join(fixture, 'commands.log')
  for (const name of ['git', 'curl', 'sha256sum', 'go']) {
    const command = name === 'sha256sum' ? 'cat >/dev/null\nexit 1' : 'exit 0'
    const path = join(fixture, name)
    writeFileSync(path, `#!/bin/sh\nprintf '%s\\n' '${name}' >> "$JIWAI_CLI_TEST_LOG"\n${command}\n`)
    chmodSync(path, 0o755)
  }
  try {
    const result = spawnSync('/bin/sh', ['-c', buildCLIInstallCommand()], {
      encoding: 'utf8',
      env: { ...process.env, PATH: `${fixture}:/usr/bin:/bin`, TMPDIR: fixture, JIWAI_CLI_TEST_LOG: log },
    })
    assert.notEqual(result.status, 0)
    assert.deepEqual(readFileSync(log, 'utf8').trim().split('\n'), ['git', 'git', 'curl', 'sha256sum'])
  } finally { rmSync(fixture, { recursive: true, force: true }) }
})

test('新二进制的版本或 MCP 验证失败时保留现有程序', () => {
  for (const failure of ['version', 'mcp']) {
    const fixture = mkdtempSync(join(tmpdir(), 'jiwai-cli-executable-test-'))
    mkdirSync(join(fixture, 'cli'))
    writeFileSync(join(fixture, 'weknora'), 'existing working binary')
    const go = join(fixture, 'go')
    writeFileSync(go, `#!/bin/sh\nwhile [ "$#" -gt 0 ]; do\n  if [ "$1" = '-o' ]; then shift; output="$1"; fi\n  shift\ndone\ncat > "$output" <<'BINARY'\n#!/bin/sh\nif [ "$1" = '--version' ] && [ "$JIWAI_CLI_TEST_FAILURE" != 'version' ]; then exit 0; fi\nexit 126\nBINARY\n`)
    chmodSync(go, 0o755)
    try {
      const command = buildCLIInstallCommand()
      const result = spawnSync('/bin/sh', ['-c', command.slice(command.indexOf('JIWAI_CLI_NEW_BINARY='))], {
        encoding: 'utf8',
        env: {
          ...process.env, PATH: `${fixture}:/usr/bin:/bin`,
          JIWAI_CLI_BIN_DIR: fixture, JIWAI_CLI_BUILD_DIR: fixture, JIWAI_CLI_TEST_FAILURE: failure,
        },
      })
      assert.equal(result.status, 126, result.stderr)
      assert.equal(readFileSync(join(fixture, 'weknora'), 'utf8'), 'existing working binary')
      assert.equal(readdirSync(fixture).some(name => name.startsWith('weknora.backup.')), false)
    } finally { rmSync(fixture, { recursive: true, force: true }) }
  }
})

test('CLI hosts preserve proxy prefixes and omit the SDK API suffix', () => {
  for (const [base, origin, host] of [
    ['https://kb.example.com/api/v1', 'https://ui.example.com', 'https://kb.example.com'],
    ['/app/weknora/api/v1', 'https://kb.example.com', 'https://kb.example.com/app/weknora'],
    ['http://127.0.0.1:19321/api/v1/', 'wails://wails.localhost', 'http://127.0.0.1:19321'],
    ['http://127.0.0.1:19321/api/v1', 'null', 'http://127.0.0.1:19321'],
  ]) {
    const command = buildCLIConnectCommand(base!, origin!)
    assert.ok(command.includes(`--host '${host}' --use &&\nweknora auth login`))
  }
})

test('unresolved desktop URLs use an explicit server placeholder', () => {
  assert.ok(buildCLIConnectCommand('/api/v1', 'null').includes("--host 'https://your-server.com'"))
  assert.ok(buildCLIConnectCommand('/api/v1', 'wails://wails.localhost').includes("--host 'https://your-server.com'"))
})

test('copyable host arguments remain literal in a POSIX shell', () => {
  const host = "https://kb.example.com/team'/$HOME/`printf-injected`/$(printf-injected)"
  const command = buildCLIConnectCommand(`${host}/api/v1`, 'https://kb.example.com')
  const argument = command.split(' --host ')[1]!.split(' --use')[0]!
  const result = spawnSync('/bin/sh', ['-c', `printf '%s' ${argument}`], { encoding: 'utf8' })
  assert.equal(result.status, 0)
  assert.equal(result.stderr, '')
  assert.equal(result.stdout, "https://kb.example.com/team'/$HOME/%60printf-injected%60/$(printf-injected)")
})
