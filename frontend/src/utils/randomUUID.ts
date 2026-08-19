interface UUIDCrypto {
  randomUUID?: () => string
  getRandomValues?: (array: Uint8Array<ArrayBuffer>) => Uint8Array<ArrayBuffer>
}

const formatUUID = (bytes: Uint8Array) => {
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, byte => byte.toString(16).padStart(2, '0'))
  return [
    hex[0], hex[1], hex[2], hex[3], '-',
    hex[4], hex[5], '-',
    hex[6], hex[7], '-',
    hex[8], hex[9], '-',
    hex[10], hex[11], hex[12], hex[13], hex[14], hex[15],
  ].join('')
}

/** 在 HTTP/IP 等不提供 crypto.randomUUID 的环境中也生成 UUID v4。 */
export function generateUUID(cryptoApi: UUIDCrypto | undefined = globalThis.crypto): string {
  if (typeof cryptoApi?.randomUUID === 'function') return cryptoApi.randomUUID()

  const bytes = new Uint8Array(new ArrayBuffer(16))
  if (typeof cryptoApi?.getRandomValues === 'function') {
    cryptoApi.getRandomValues(bytes)
  } else {
    for (let index = 0; index < bytes.length; index++) {
      bytes[index] = Math.floor(Math.random() * 256)
    }
  }
  return formatUUID(bytes)
}
