import { test, expect } from 'vitest';

test('runs in another shard', () => expect(2 + 2).toBe(4));
