// jest-expo preset (docs/spec/phone.md P1). Tests never touch the network: test/setup.ts
// replaces every native module with a recording fake and fails any real fetch.
module.exports = {
  preset: 'jest-expo',
  setupFilesAfterEnv: ['<rootDir>/test/setup.ts'],
  testMatch: ['<rootDir>/**/*.test.ts', '<rootDir>/**/*.test.tsx'],
  testPathIgnorePatterns: ['/node_modules/', '<rootDir>/android/', '<rootDir>/ios/', '<rootDir>/.expo/'],
  modulePathIgnorePatterns: ['<rootDir>/android/', '<rootDir>/ios/'],
  clearMocks: true,
};
