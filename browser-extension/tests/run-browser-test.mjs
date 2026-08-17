import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { createServer } from 'node:http'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, extname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const testDir = dirname(fileURLToPath(import.meta.url))
const extensionDir = resolve(testDir, '..')
const chromePath = process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const profileDir = await mkdtemp(join(tmpdir(), 'jiwai-extension-profile-'))
const screenshotPath = join(profileDir, 'fixture.png')

let resolveResult
const resultPromise = new Promise((resolveResultPromise) => {
  resolveResult = resolveResultPromise
})

const mimeTypes = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8'
}

const server = createServer(async (request, response) => {
  const requestUrl = new URL(request.url || '/', 'http://127.0.0.1')
  if (requestUrl.pathname === '/__result') {
    const result = {
      status: requestUrl.searchParams.get('status') || 'fail',
      details: requestUrl.searchParams.get('details') || ''
    }
    response.writeHead(204)
    response.end()
    resolveResult(result)
    return
  }

  const pathname = requestUrl.pathname === '/' ? '/tests/extractor-fixtures.html' : requestUrl.pathname
  const filePath = resolve(extensionDir, '.' + decodeURIComponent(pathname))
  if (!filePath.startsWith(extensionDir + '/')) {
    response.writeHead(403)
    response.end('Forbidden')
    return
  }

  try {
    const content = await readFile(filePath)
    response.writeHead(200, { 'Content-Type': mimeTypes[extname(filePath)] || 'application/octet-stream' })
    response.end(content)
  } catch (error) {
    response.writeHead(404)
    response.end('Not found')
  }
})

await new Promise((resolveListen) => server.listen(0, '127.0.0.1', resolveListen))
const address = server.address()
const port = typeof address === 'object' && address ? address.port : 0

const chrome = spawn(chromePath, [
  '--headless=new',
  '--disable-gpu',
  '--no-first-run',
  '--no-default-browser-check',
  `--user-data-dir=${profileDir}`,
  '--virtual-time-budget=7000',
  `--screenshot=${screenshotPath}`,
  '--window-size=900,500',
  `http://127.0.0.1:${port}/tests/extractor-fixtures.html`
], { stdio: ['ignore', 'ignore', 'pipe'] })

let browserErrors = ''
chrome.stderr.on('data', (chunk) => { browserErrors += chunk.toString() })

let timeoutId
const timeoutPromise = new Promise((_, reject) => {
  timeoutId = setTimeout(() => reject(new Error('动态网页采集测试超时')), 20000)
})

try {
  const result = await Promise.race([resultPromise, timeoutPromise])
  if (result.status !== 'pass') {
    throw new Error(result.details || '动态网页采集断言失败')
  }
  console.log('EXTENSION_BROWSER_TEST_OK')
} catch (error) {
  if (browserErrors.trim()) process.stderr.write(browserErrors)
  throw error
} finally {
  clearTimeout(timeoutId)
  await new Promise((resolveClose) => server.close(resolveClose))
  if (chrome.exitCode === null) {
    chrome.kill('SIGTERM')
    await Promise.race([
      once(chrome, 'exit'),
      new Promise((resolveKill) => setTimeout(resolveKill, 2000))
    ])
    if (chrome.exitCode === null) chrome.kill('SIGKILL')
  }
  await rm(profileDir, { recursive: true, force: true })
}
