import '@testing-library/jest-dom/vitest';
import { afterEach } from 'vitest';
import { clearQueryCache } from '../src/queryCache.ts';
afterEach(() => clearQueryCache());
