import '@testing-library/jest-dom/vitest';
import { afterEach } from 'vitest';
import { cleanup } from '@testing-library/react';

// globals are off, so Testing Library can't register its own cleanup.
afterEach(() => {
  cleanup();
});
