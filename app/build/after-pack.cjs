'use strict';
// electron-builder afterPack hook: flips the Electron fuses of the packaged binary
// (docs/spec/desktop.md C2). It runs before any signing; there is no signing in phase 6.
//
// @electron/fuses 2.x is ESM only and needs Node >= 22.12, so it is loaded with import().

const path = require('node:path');

/**
 * The fuses of C2 by name (FuseV1Options keys). EnableCookieEncryption stays off: existing and
 * imported sign-ins are stored unencrypted (open risk 6).
 */
const FUSES = Object.freeze({
  RunAsNode: false,
  EnableCookieEncryption: false,
  EnableNodeOptionsEnvironmentVariable: false,
  EnableNodeCliInspectArguments: false,
  EnableEmbeddedAsarIntegrityValidation: true,
  OnlyLoadAppFromAsar: true,
  GrantFileProtocolExtraPrivileges: false,
});

/** Path of the Electron executable inside appOutDir, by platform. */
function executablePath(context) {
  const { electronPlatformName: platform, appOutDir, packager } = context;
  const name = packager.appInfo.productFilename;
  if (platform === 'darwin') return path.join(appOutDir, `${name}.app`, 'Contents', 'MacOS', name);
  if (platform === 'win32') return path.join(appOutDir, `${name}.exe`);
  return path.join(appOutDir, packager.executableName || name.toLowerCase());
}

/** The config object for flipFuses, given the FuseV1Options and FuseVersion of the library. */
function buildConfig(FuseV1Options, FuseVersion, { resetAdHocDarwinSignature = false } = {}) {
  const config = { version: FuseVersion.V1, resetAdHocDarwinSignature };
  for (const [key, on] of Object.entries(FUSES)) {
    if (!(key in FuseV1Options)) throw new Error(`after-pack: unknown fuse ${key}`);
    config[FuseV1Options[key]] = on;
  }
  return config;
}

async function afterPack(context) {
  const { flipFuses, FuseVersion, FuseV1Options } = await import('@electron/fuses');
  // An unsigned arm64 macOS binary does not run; flipping invalidates the linker's ad-hoc
  // signature, so ask the library to re-create it. (Real signing is a manual step.)
  const arm64Mac = context.electronPlatformName === 'darwin' && context.arch === 3; // Arch.arm64
  const exe = executablePath(context);
  await flipFuses(exe, buildConfig(FuseV1Options, FuseVersion, { resetAdHocDarwinSignature: arm64Mac }));
}

module.exports = afterPack;
module.exports.default = afterPack;
module.exports.FUSES = FUSES;
module.exports.executablePath = executablePath;
module.exports.buildConfig = buildConfig;
