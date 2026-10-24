// The wy-scan module's JS API (docs/spec/phone.md P2): the pairing QR scanner. Android only;
// where the native module is missing (iOS, Expo Go) `available` is false and scanning resolves
// "unavailable", so the Pair screen offers typed input only.
import { requireOptionalNativeModule } from 'expo';
import { Platform } from 'react-native';

export type CameraPermission = { granted: boolean; canAskAgain: boolean };

export type ScanResult =
  | { status: 'scanned'; text: string }
  | { status: 'cancelled' }
  | { status: 'denied'; canAskAgain: boolean }
  | { status: 'unavailable' };

type Native = {
  cameraPermission(): Promise<CameraPermission>;
  scan(prompt: string): Promise<ScanResult>;
};

const native: Native | null = Platform.OS === 'android' ? requireOptionalNativeModule<Native>('WyScan') : null;

/** True where the module exists (an Android build that includes it). */
export const available = native !== null;

/** Whether the camera may be used, without asking. */
export async function cameraPermission(): Promise<CameraPermission> {
  return native ? native.cameraPermission() : { granted: false, canAskAgain: false };
}

/**
 * Asks for the camera when needed, then opens the full-screen scanner (QR codes only) with
 * `prompt` under the viewfinder, and resolves with the first code's text or why there is none.
 */
export async function scanQr(prompt: string): Promise<ScanResult> {
  return native ? native.scan(prompt) : { status: 'unavailable' };
}
