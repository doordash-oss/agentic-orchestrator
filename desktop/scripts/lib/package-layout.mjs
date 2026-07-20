import { join } from 'node:path';

export function unpackedExecutablePath(
  desktopDir,
  platform = process.platform,
  arch = process.arch,
) {
  if (platform === 'darwin') {
    return join(
      desktopDir,
      'dist',
      'mac-universal',
      'Agentico.app',
      'Contents',
      'MacOS',
      'Agentico',
    );
  }
  if (platform === 'linux') {
    const unpackedDir = arch === 'arm64' ? 'linux-arm64-unpacked' : 'linux-unpacked';
    return join(desktopDir, 'dist', unpackedDir, 'agentico');
  }
  throw new Error(`unsupported packaged app host: ${platform}`);
}
