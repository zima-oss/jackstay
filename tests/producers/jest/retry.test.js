let attempts = 0;

jest.retryTimes(1);
test('passes on the second attempt', () => {
  attempts++;
  expect(attempts).toBe(2);
});
