import SwiftUI
import UIKit

final class AppOrientationDelegate: NSObject, UIApplicationDelegate {
    static var orientationLock: UIInterfaceOrientationMask = .portrait

    func application(
        _ application: UIApplication,
        supportedInterfaceOrientationsFor window: UIWindow?
    ) -> UIInterfaceOrientationMask {
        Self.orientationLock
    }
}

@main
struct ZeroDeskApp: App {
    @UIApplicationDelegateAdaptor(AppOrientationDelegate.self) private var appDelegate

    var body: some Scene {
        WindowGroup {
            BedsideControlView()
                .preferredColorScheme(.dark)
        }
    }
}
