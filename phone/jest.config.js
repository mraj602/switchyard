// jest-expo preset (docs/spec/phone.md P1). Tests never touch the network: test/setup.ts
// replaces every native module with a recording fake and fails any real fetch.
module.exports = {
  preset: 'jest-expo',
  setupFilesAfterEnv: ['<rootDir>/test/setup.ts'],
  testMatch: ['<rootDir>/**/*.test.ts', '<rootDir>/**/*.test.tsx'],
  testPathIgnorePatterns: ['/node_modules/', '<rootDir>/android/', '<rootDir>/ios/', '<rootDir>/.expo/'],
  modulePathIgnorePatterns: ['<rootDir>/android/', '<rootDir>/ios/'],
  clearMocks: true,
  // uuid 11 (overridden for xcode, see package.json) resolves to its ESM browser build under
  // jest-expo; the config-plugin tests load it through xcode, so use its CommonJS build.
  moduleNameMapper: { '^uuid$': require.resolve('uuid') /* Node's "require" condition: the CommonJS build */ },
  // Headroom for starved workers; see the timing note at the bottom of test/setup.ts.
  testTimeout: 30000,
};
