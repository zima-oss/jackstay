package dev.jackstay.fixtures;

import org.testng.annotations.Test;

import static org.testng.Assert.fail;

public class FailingTest {
    @Test
    public void fails() {
        fail("fixture failure");
    }
}
