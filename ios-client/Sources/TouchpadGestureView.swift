import SwiftUI
import UIKit

struct TouchpadGestureView: UIViewRepresentable {
    var onMove: (Int, Int) -> Void
    var onButton: (String, String) -> Void
    var onScroll: (Int) -> Void
    @Binding var isDraggingActive: Bool

    func makeUIView(context: Context) -> TouchpadUIView {
        let view = TouchpadUIView()
        view.onMove = onMove
        view.onButton = onButton
        view.onScroll = onScroll
        view.onDragStateChanged = { dragging in
            DispatchQueue.main.async {
                self.isDraggingActive = dragging
            }
        }
        return view
    }

    func updateUIView(_ uiView: TouchpadUIView, context: Context) {
        uiView.onMove = onMove
        uiView.onButton = onButton
        uiView.onScroll = onScroll
    }
}

final class TouchpadUIView: UIView {
    var onMove: ((Int, Int) -> Void)?
    var onButton: ((String, String) -> Void)?
    var onScroll: ((Int) -> Void)?
    var onDragStateChanged: ((Bool) -> Void)?

    private let lightImpact = UIImpactFeedbackGenerator(style: .light)
    private let mediumImpact = UIImpactFeedbackGenerator(style: .medium)

    private var trackedTouches: [UITouch: CGPoint] = [:]
    private var touchStartTime: CFTimeInterval = 0
    private var maxFingers: Int = 0
    private var totalDistance: CGFloat = 0
    private var lastTapTime: CFTimeInterval = 0
    private var isDragging: Bool = false

    private var accumX: CGFloat = 0
    private var accumY: CGFloat = 0
    private var accumScrollY: CGFloat = 0

    override init(frame: CGRect) {
        super.init(frame: frame)
        isMultipleTouchEnabled = true
        backgroundColor = .clear
        lightImpact.prepare()
        mediumImpact.prepare()
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent?) {
        let now = CACurrentMediaTime()
        let currentCount = (event?.allTouches?.filter { $0.phase != .ended && $0.phase != .cancelled }.count) ?? touches.count

        if trackedTouches.isEmpty {
            touchStartTime = now
            maxFingers = currentCount
            totalDistance = 0

            if currentCount == 1 && (now - lastTapTime) < 0.26 {
                isDragging = true
                onDragStateChanged?(true)
                lightImpact.impactOccurred()
                onButton?("left", "down")
            }
        } else {
            maxFingers = max(maxFingers, currentCount)
        }

        for t in touches {
            trackedTouches[t] = t.location(in: self)
        }
    }

    override func touchesMoved(_ touches: Set<UITouch>, with event: UIEvent?) {
        let active = event?.allTouches?.filter { $0.phase != .ended && $0.phase != .cancelled } ?? []

        if active.count == 1, let t = active.first, let prev = trackedTouches[t] {
            let cur = t.location(in: self)
            let rawDx = cur.x - prev.x
            let rawDy = cur.y - prev.y
            let speed = hypot(rawDx, rawDy)
            totalDistance += speed

            // Velocity-aware acceleration curve for smooth one-handed 2560x1440 cursor precision
            let gain: CGFloat
            if speed > 14 {
                gain = 2.25
            } else if speed > 5 {
                gain = 1.60
            } else {
                gain = 1.15
            }

            accumX += rawDx * gain
            accumY += rawDy * gain
            let dx = Int(accumX)
            let dy = Int(accumY)
            if dx != 0 || dy != 0 {
                accumX -= CGFloat(dx)
                accumY -= CGFloat(dy)
                onMove?(dx, dy)
            }
            trackedTouches[t] = cur
        } else if active.count == 2 {
            var sumDy: CGFloat = 0
            var count: CGFloat = 0
            for t in active {
                if let prev = trackedTouches[t] {
                    let cur = t.location(in: self)
                    let dy = cur.y - prev.y
                    totalDistance += abs(dy)
                    sumDy += dy
                    count += 1
                    trackedTouches[t] = cur
                }
            }
            if count > 0 {
                accumScrollY += (sumDy / count) * 6.5
                let wheel = Int(accumScrollY)
                if abs(wheel) >= 12 {
                    accumScrollY -= CGFloat(wheel)
                    onScroll?(wheel)
                }
            }
        }
    }

    override func touchesEnded(_ touches: Set<UITouch>, with event: UIEvent?) {
        for t in touches {
            trackedTouches.removeValue(forKey: t)
        }

        if trackedTouches.isEmpty {
            let dt = CACurrentMediaTime() - touchStartTime
            if isDragging {
                isDragging = false
                onDragStateChanged?(false)
                onButton?("left", "up")
            } else if dt < 0.24 && totalDistance < 12 {
                if maxFingers == 1 {
                    lightImpact.impactOccurred()
                    onButton?("left", "click")
                    lastTapTime = CACurrentMediaTime()
                } else if maxFingers == 2 {
                    mediumImpact.impactOccurred()
                    onButton?("right", "click")
                }
            }
            accumX = 0
            accumY = 0
            accumScrollY = 0
        }
    }

    override func touchesCancelled(_ touches: Set<UITouch>, with event: UIEvent?) {
        for t in touches {
            trackedTouches.removeValue(forKey: t)
        }
        if trackedTouches.isEmpty && isDragging {
            isDragging = false
            onDragStateChanged?(false)
            onButton?("left", "up")
        }
    }
}
