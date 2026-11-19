describe('group', () => {
  test('brackets [1]', () => expect(3).toBe(3));
  test('brackets 1', () => { throw new Error('unselected case ran'); });
});
