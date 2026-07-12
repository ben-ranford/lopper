import { realpath, stat } from "node:fs/promises";

export async function binaryFileSignature(binaryPath: string): Promise<string> {
  const resolvedPath = await realpath(binaryPath);
  const details = await stat(resolvedPath, { bigint: true });
  return [
    resolvedPath,
    details.dev,
    details.ino,
    details.size,
    details.mtimeNs,
    details.ctimeNs,
  ].join("\0");
}

export async function tryBinaryFileSignature(binaryPath: string): Promise<string | undefined> {
  try {
    return await binaryFileSignature(binaryPath);
  } catch {
    return undefined;
  }
}
