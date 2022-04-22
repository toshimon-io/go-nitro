import config from './jest.config';

config.testMatch = ['<rootDir>/gas-benchmarks/**/*.test.ts'];
config.reporters = ['default'];
config.setupFilesAfterEnv = ['<rootDir>/gas-benchmarks/vanillaSetup.ts'];
config.maxConcurrency = 1;

export default config;
