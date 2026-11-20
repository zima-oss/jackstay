package dev.jackstay.fixtures;

import static org.junit.jupiter.api.Assertions.fail;

import org.junit.jupiter.api.Test;

class AlwaysFailingTest {
    @Test
    void failsEveryRun() {
        fail("every attempt");
    }
}
