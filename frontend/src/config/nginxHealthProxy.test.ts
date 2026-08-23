import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const nginxConfig = readFileSync(new URL('../../nginx.conf', import.meta.url), 'utf8')

test('public health endpoint proxies the backend instead of the SPA fallback', () => {
  const healthLocation = nginxConfig.match(/location = \/health \{([\s\S]*?)\n\s*\}/)?.[1]
  assert.ok(healthLocation, 'nginx must define an exact /health location')
  assert.match(
    healthLocation,
    /proxy_pass \$\{APP_SCHEME\}:\/\/\$\{APP_HOST\}:\$\{APP_PORT\}\/health;/,
  )
  assert.doesNotMatch(healthLocation, /try_files|index\.html/)

  const healthIndex = nginxConfig.indexOf('location = /health')
  const spaIndex = nginxConfig.indexOf('location / {')
  assert.ok(healthIndex >= 0 && healthIndex < spaIndex, 'exact health route must precede SPA fallback')
})
