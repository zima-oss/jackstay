package dev.jackstay.fixtures;

import org.testng.SkipException;
import org.testng.annotations.Test;

public class PassingTest {
    @Test
    public void passes() {}

    @Test
    public void skipped() {
        throw new SkipException("fixture skip");
    }
}
