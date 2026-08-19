import { sha256 } from '@noble/hashes/sha2.js'
import { bytesToHex } from '@noble/hashes/utils.js'

/**
 * 计算单个上传分片的 SHA-256。安全上下文优先使用浏览器 Web Crypto，
 * 普通 HTTP 部署则回退到经过审计的纯 JavaScript 实现。
 */
export async function sha256BlobHex(
  blob: Blob,
  subtle: SubtleCrypto | undefined = globalThis.crypto?.subtle,
): Promise<string> {
  const bytes = new Uint8Array(await blob.arrayBuffer())
  if (subtle) {
    try {
      return bytesToHex(new Uint8Array(await subtle.digest('SHA-256', bytes)))
    } catch {
      // 某些 WebView 暴露 subtle 但禁用 digest，继续使用兼容实现。
    }
  }
  return bytesToHex(sha256(bytes))
}
