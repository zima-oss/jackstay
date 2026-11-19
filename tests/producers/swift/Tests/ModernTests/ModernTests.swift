import Foundation
import Testing

@Test func modernPass() {
    #expect(2 + 2 == 4)
}

@Test(arguments: [1, 2]) func parameterized(_ number: Int) {
    #expect(number > 0)
}

@Test(.disabled("pilot skip")) func modernSkip() {}

@Test func modernOptionalFailure() {
    #expect(ProcessInfo.processInfo.environment["JACKSTAY_SWIFT_FAIL"] != "1")
}
