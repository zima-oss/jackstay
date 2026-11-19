[TestClass]
public sealed class RetryCases
{
    private static int attempts;

    [TestMethod]
    [Retry(1)]
    public void PassesOnRetry()
    {
        if (System.Threading.Interlocked.Increment(ref attempts) == 1)
        {
            Assert.Fail("first attempt");
        }
    }
}
