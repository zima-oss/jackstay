import Foundation
import XCTest

final class LegacyTests: XCTestCase {
    func testPass() {
        XCTAssertEqual(2 + 2, 4)
    }

    func testSkip() throws {
        throw XCTSkip("pilot skip")
    }

    func testOptionalFailure() {
        XCTAssertNotEqual(ProcessInfo.processInfo.environment["JACKSTAY_SWIFT_FAIL"], "1")
    }
}
