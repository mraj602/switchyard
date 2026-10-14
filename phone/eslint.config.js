// ESLint flat config (docs/spec/phone.md P1): eslint-config-expo, plus
// - no console in src/** (logs never carry the token or hub bodies, P3);
// - src/lib/** is pure: no react, react-native, expo-* or @expo/* import
//   (test/imports.test.ts checks the full import graph too).
const { defineConfig } = require('eslint/config');
const expoConfig = require('eslint-config-expo/flat');

const PURE_FORBIDDEN = {
  patterns: [
    { regex: '^react$|^react/|^react-native($|/)|^react-native-', message: 'src/lib is pure: take platform services as arguments (phone.md P1).' },
    { regex: '^expo($|-|/)|^@expo/|^@react-native', message: 'src/lib is pure: take platform services as arguments (phone.md P1).' },
  ],
};

module.exports = defineConfig([
  expoConfig,
  {
    ignores: ['node_modules/**', 'android/**', 'ios/**', '.expo/**', 'dist/**', 'coverage/**'],
  },
  {
    files: ['src/**/*.{ts,tsx,js}'],
    rules: { 'no-console': 'error' },
  },
  {
    files: ['src/lib/**/*.{ts,tsx,js}'],
    ignores: ['src/lib/**/*.test.{ts,tsx}'],
    rules: { 'no-restricted-imports': ['error', PURE_FORBIDDEN] },
  },
]);
