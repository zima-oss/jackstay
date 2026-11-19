import { test, expect } from 'vitest';

let attempts = 0;
test('passes on the second attempt', () => {
  attempts++;
  expect(attempts).toBe(2);
});
