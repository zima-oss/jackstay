[TestClass]
public sealed class Cases
{
    [TestMethod]
    public void Success() => Assert.IsTrue(new[] { 1, 2 }.Contains(2));

    [TestMethod]
    [Ignore("fixture")]
    public void Skipped() => Assert.Fail();

    [TestMethod]
    public void Failure() => Assert.Fail("fixture");
}
