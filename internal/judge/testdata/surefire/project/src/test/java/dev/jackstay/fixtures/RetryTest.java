package dev.jackstay.fixtures;

import org.testng.IRetryAnalyzer;
import org.testng.ITestResult;
import org.testng.annotations.Test;

import static org.testng.Assert.fail;

public class RetryTest {
    private static int attempts;

    public static class RetryOnce implements IRetryAnalyzer {
        private boolean retried;

        @Override
        public boolean retry(ITestResult result) {
            if (retried) {
                return false;
            }
            retried = true;
            return true;
        }
    }

    @Test(retryAnalyzer = RetryOnce.class)
    public void passesOnRetry() {
        if (++attempts == 1) {
            fail("first attempt");
        }
    }
}
