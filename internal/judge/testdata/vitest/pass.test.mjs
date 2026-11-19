import { test, expect } from 'vitest';

test('passes', () => expect(2 + 2).toBe(4));
test.skip('skips', () => {});
