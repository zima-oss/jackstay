package dev.jackstay.fixtures;

import static org.junit.jupiter.api.Assertions.fail;

import org.junit.jupiter.api.Test;

class FlakyTest {
    private static int attempts;

    @Test
    void passesOnRerun() {
        if (++attempts == 1) {
            fail("first attempt");
        }
    }

    @Test
    void passes() {
    }
}
