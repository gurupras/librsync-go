import fs from 'fs'
import path from 'path'

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from 'vitest'
import puppeteer, { Browser, Page } from 'puppeteer'
import http from 'http'
import express from 'express'
import portfinder from 'portfinder'
import { Delta as DeltaCls, ISignature, SigType, Signature as SignatureCls, SignatureType } from './lib'
import { allTestCases, argsFromTestName } from './test-helpers'


const dirname = __dirname
const rootDir = path.resolve(__dirname, '..')
const testdataDir = path.join(rootDir, 'testdata')

declare global {
  interface Window {
    Signature: typeof SignatureCls;
    Delta: typeof DeltaCls;
    signatureMap: Map<String, ISignature>
    deltaMap: Map<String, DeltaCls>
  }
}

describe('librsync.wasm', () => {
  let browser: Browser
  let page: Page
  let server: Awaited<ReturnType<typeof startServer>>

    beforeAll(async () => {
    browser = await puppeteer.launch({ headless: 'new', devtools: true })
    server = await startServer()
  })

  beforeEach(async () => {
    page = await browser.newPage()
    await page.goto(`${server.address}/index.html`)
    await page.waitForFunction(() => window.librsync?.Signature)
    await page.evaluate(() => {
      window.signatureMap = new Map()
      window.deltaMap = new Map()
    })
  })

  afterEach(async () => {
    await page.close()
  })

  afterAll(async () => {
    await browser.close()
    await server.stop()
  })

  describe('Signature', () => {
    beforeEach(async () => {
    })

    test.each(allTestCases)('Able to compute signature %s', async (label) => {
      const [filePath, magic, blockLen, strongLen] = argsFromTestName(label)
      const content = fs.readFileSync(`${filePath}.old`)
      const data = Array.from(content.values())
      let sig = await createSignature(blockLen, strongLen, magic)
      await signatureDigest(sig, data, blockLen)
      let result = await sig.end()

      expect(result.blockLen).toEqual(blockLen)
      expect(result.strongLen).toEqual(strongLen)
      expect(result.sigType).toEqual(magic)
      expect(result.weak2block.size).toEqual(Math.ceil(data.length / blockLen))

      let serialized = await sig.serialize()
      const expectedBuffer = fs.readFileSync(path.resolve(testdataDir, `${label}.signature`))
      const expected = new Uint8Array(Array.from(expectedBuffer.values()))
      expect(serialized.length).toEqual(expected.length)

      // Ensure full data digested once equals the same result
      sig = await createSignature(blockLen, strongLen, magic)
      await signatureDigest(sig, data, data.length)
      result = await sig.end()

      expect(result.blockLen).toEqual(blockLen)
      expect(result.strongLen).toEqual(strongLen)
      expect(result.sigType).toEqual(magic)
      expect(result.weak2block.size).toEqual(Math.ceil(data.length / blockLen))

      serialized = await sig.serialize()
      expect(serialized.length).toEqual(expected.length)
    })
  })

  describe('Delta', () => {
    test.each(allTestCases)('Able to compute delta %s', async (label) => {
      const [filePath, magic, blockLen, strongLen] = argsFromTestName(label)
      const content = fs.readFileSync(`${filePath}.new`)
      const sigBuffer = fs.readFileSync(path.resolve(testdataDir, `${label}.signature`))
      const sigBytes = new Uint8Array(Array.from(sigBuffer.values()))
      const chunkedDelta = await createDelta(sigBytes, 64 * 1024)
      const chunkedDeltaBytes = await deltaDigest(chunkedDelta, Array.from(content.values()), 512)
      
      const fullDelta = await createDelta(sigBytes, 16 * 1024 * 1024)
      const fullDeltaBytes = await deltaDigest(fullDelta, Array.from(content.values()), content.length)
      expect(chunkedDeltaBytes.length).toEqual(fullDeltaBytes.length)
    })
  })

  const splitArrayIntoChunks = <T>(array: T[], chunkSize: number) => {
    const result: T[][] = [];
    for (let i = 0; i < array.length; i += chunkSize) {
      result.push(array.slice(i, i + chunkSize));
    }
    return result;
  }

  const signatureDigest = async (sig: ISignature, data: number[], blockLen: number) => {
    const chunks = splitArrayIntoChunks(data, blockLen)
    for (let chunk of chunks) {
      await sig.digest(new Uint8Array(chunk))
    }
  }

  const createSignature = async (blockLen: number, strongLen: number, sigType: SigType) => {
    const id = await page.evaluate((blockLen, strongLen, sigType) => {
      const id = `${Math.random() * 1e9 | 0}`
      const sig = new window.Signature(blockLen, strongLen, sigType)
      window.signatureMap.set(id, sig)
      return id
    }, blockLen, strongLen, sigType)

    const sig: ISignature = {
      async digest(data) {
          return page.evaluate(async (id, array) => {
            // This needs to be converted back to Uint8Array
            const data = new Uint8Array(array)
            const sig = window.signatureMap.get(id)
            await sig!.digest(data)
          }, id, Array.from(data))
      },
      async end() {
        const result = await page.evaluate(async (id) => {
          const sig = window.signatureMap.get(id)
          const result = await sig!.end()
          // @ts-ignore
          result.weak2block = Object.fromEntries(result.weak2block.entries())
          return result
        }, id)
        const weak2blockObj: Record<number, number> = result.weak2block as any
        result.weak2block = new Map(Object.entries(weak2blockObj)) as any
        return result
      },
      async serialize() {
        const dataObj = await page.evaluate((id) => {
          const sig = window.signatureMap.get(id)
          return sig!.serialize()
        }, id)
        const array: number[] = []
        Object.values(dataObj).forEach(x => array.push(x))
        const data = new Uint8Array(array)
        return data
      },
    }
    return sig
  }

  const deserializeSignature = async (data: Uint8Array) => {
    const [signature, error] = await page.evaluate(async (data) => {
      return window.librsync.Signature.deserialize(new Uint8Array(data))
    }, Array.from(data))
    if (error) {
      throw new Error(error)
    }
    return signature as SignatureType
  }

  const createDelta = async (signatureBytes: Uint8Array, literalBufSize: number) => {
    const id = await page.evaluate(async (signatureBytes, literalBufSize) => {
      const id = `${Math.random() * 1e9 | 0}`
      const [signature] = librsync.Signature.deserialize(new Uint8Array(signatureBytes))
      const delta = new window.Delta(signature!, literalBufSize)
      window.deltaMap.set(id, delta)
      return id
    }, Array.from(signatureBytes), literalBufSize)

    const delta = {
      async digest(data: Uint8Array) {
        const dataObj = await page.evaluate(async (id, data) => {
          const bytes = new Uint8Array(data)
          const delta = window.deltaMap.get(id)!
          const result = delta.digest(bytes)
          return result
        }, id, Array.from(data))
        const resultArray: number[] = []
        Object.values(dataObj).forEach(x => resultArray.push(x))
        const result = new Uint8Array(resultArray)
        return result
      },
      async end() {
        const dataObj = await page.evaluate(async (id) => {
          const delta = window.deltaMap.get(id)!
          const result = delta.end()
          return result
        }, id)
        const resultArray: number[] = []
        Object.values(dataObj).forEach(x => resultArray.push(x))
        const result = new Uint8Array(resultArray)
        return result
      }
    }
    return delta
  }

  const deltaDigest = async (delta: Awaited<ReturnType<typeof createDelta>>, data: number[], chunkSize = data.length) => {
    const chunks = splitArrayIntoChunks(data, chunkSize)
    const array: Uint8Array[] = []
    let len = 0
    for (let chunk of chunks) {
      const bytes = await delta.digest(new Uint8Array(chunk))
      array.push(bytes)
      len += bytes.length
    }
    const final = await delta.end()
    array.push(final)
    len += final.length

    const result = new Uint8Array(len)
    let offset = 0
    for (const entry of array) {
      result.set(entry, offset)
      offset += entry.length
    }
    return result
  }
})

async function startServer () {
  const app = express()
  app.use('/', express.static('./test-resources'))
  const server = http.createServer(app)
  const port = await portfinder.getPortPromise()
  server.listen(port)
  return {
    address: `http://localhost:${port}`,
    stop: () => {
      return new Promise(resolve => {
        server.close(resolve)
      })
    }
  }
}
