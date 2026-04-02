import path from 'path'
import { SigType } from './lib';

export function argsFromTestName(name: string): [string, SigType, number, number] {
  const segs: string[] = name.split("-");
  if (segs.length !== 4) {
    throw new Error(`Invalid format for name ${name}`);
  }

  const file: string = path.resolve(__dirname, '..', 'testdata', segs[0]);

  let magic: SigType;
  switch (segs[1]) {
    case "blake2":
      magic = SigType.BLAKE2_SIG_MAGIC;
      break;
    case "md4":
      magic = SigType.MD4_SIG_MAGIC;
      break;
    default:
      throw new Error(`Invalid magic ${segs[1]}`);
  }

  const blockLen: number = parseInt(segs[2], 10);
  if (isNaN(blockLen)) {
    throw new Error(`Invalid block length ${segs[2]}`);
  }

  const strongLen: number = parseInt(segs[3], 10);
  if (isNaN(strongLen)) {
    throw new Error(`Invalid strong hash length ${segs[3]}`);
  }

  return [file, magic, blockLen, strongLen];
}


export const allTestCases: string[] = [
  "000-blake2-11-23",
  "000-blake2-512-32",
  "000-md4-256-7",
  "001-blake2-512-32",
  "001-blake2-776-31",
  "001-md4-777-15",
  "002-blake2-512-32",
  "002-blake2-431-19",
  "002-md4-128-16",
  "003-blake2-512-32",
  "003-blake2-1024-13",
  "003-md4-1024-13",
  "004-blake2-1024-28",
  "004-blake2-2222-31",
  "004-blake2-512-32",
  "005-blake2-512-32",
  "005-blake2-1000-18",
  "005-md4-999-14",
  "006-blake2-2-32",
  "007-blake2-5-32",
  "007-blake2-4-32",
  "007-blake2-3-32",
  "008-blake2-222-30",
  "008-blake2-512-32",
  "008-md4-111-11",
  "009-blake2-2048-26",
  "009-blake2-512-32",
  "009-md4-2033-15",
  "010-blake2-512-32",
  "010-blake2-7-6",
  "010-md4-4096-8",
  "011-blake2-3-32",
  "011-md4-3-9",
];
