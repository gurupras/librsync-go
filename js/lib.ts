export enum SigType {
  BLAKE2_SIG_MAGIC = 0x72730137,
  MD4_SIG_MAGIC = 0x72730136
}

interface _SignatureType {
  sigType: SigType
  blockLen: number
  strongLen: number
  weak2block: Map<number, number>
  strongSigs: Uint8Array[]
}
export type SignatureType = _SignatureType

export interface ISignature {
  digest: (data: Uint8Array) => void | Promise<void>
  end(): SignatureType | Promise<SignatureType>
  serialize(): Uint8Array | Promise<Uint8Array>
}

export class Signature implements ISignature {
  private _sig: ISignature

  constructor(blockLen: number, strongLen: number, sigType: SigType) {
    this._sig = new librsync.Signature(blockLen, strongLen, sigType)
  }

  digest (data: Uint8Array) {
    this._sig.digest(data)
  }

  end() {
    return this._sig.end()
  }

  serialize() {
    return this._sig.serialize()
  }
}

export class Delta {
  private _delta: librsync.Delta

  constructor (sig: SignatureType, literalBufSize: number) {
    console.log(`Size of sig.weak2block: ${sig.weak2block.size}`)
    this._delta = new librsync.Delta(sig, literalBufSize)
  }

  digest (data: Uint8Array): Uint8Array {
    const result = this._delta.digest(data)
    const [bytes, len, error] = result
    if (error) {
      throw new Error(error)
    }
    return bytes!.slice(0, len)
  }


  end (): Uint8Array {
    const [bytes, len, error] = this._delta.end()
    if (error) {
      throw new Error(error)
    }
    return bytes!.slice(0, len)
  }
}

type DeltaResponse = [Uint8Array?, number?, string?]

declare global {
  namespace librsync {
    export type SignatureType = _SignatureType
    class Signature implements ISignature {
      constructor(blockLen: number, strongLen: number, sigType: SigType)
      digest: (data: Uint8Array) => void
      end(): SignatureType
      serialize(): Uint8Array
      static deserialize(data: Uint8Array): [SignatureType?, string?]
    }

    class Delta {
      constructor(sig: SignatureType, literalBufSize: number)
      digest: (data: Uint8Array) => DeltaResponse
      end(): DeltaResponse
    }
    const BLAKE2_SIG_MAGIC = 0x72730137
    const MD4_SIG_MAGIC = 0x72730136
  }
}
