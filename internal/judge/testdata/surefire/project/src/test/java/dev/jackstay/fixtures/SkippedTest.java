package dev.jackstay.fixtures;

import org.testng.SkipException;
import org.testng.annotations.Test;

public class SkippedTest {
    @Test
    public void skipped() {
        throw new SkipException("fixture skip");
    }
}
