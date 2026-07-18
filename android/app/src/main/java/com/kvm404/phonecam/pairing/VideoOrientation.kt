package com.kvm404.phonecam.pairing

/**
 * How the phone maps the laptop's advertised (landscape) [VideoProfile] onto the encoded
 * stream. The choice determines the dimensions announced in `/pair`, so it must be locked
 * in before the pairing request is sent.
 */
enum class OrientationMode {
    /** Follow the camera: swap to portrait dims when the buffer needs a 90/270° rotation. */
    AUTO,

    /** Always stream portrait dims (the base profile's shorter edge as width). */
    PORTRAIT,

    /** Always stream landscape dims (the base profile's longer edge as width). */
    LANDSCAPE,
}

/**
 * The concrete encoder target: the dims announced at `/pair` and fed to the codec, plus the
 * rotation that the streaming analyzer must apply to the (landscape) camera buffer so the
 * cropped frame lands on exactly those dims.
 */
data class EffectiveVideo(
    val profile: VideoProfile,
    /** Clockwise rotation (0/90/180/270) to apply to the cropped buffer. */
    val rotationDegrees: Int,
)

/**
 * Pure, Android-free resolver from an [OrientationMode] choice + the camera buffer rotation
 * to the concrete [EffectiveVideo] (dims + rotation-to-apply). JVM-testable.
 *
 * The camera delivers landscape buffers; [bufferRotation] is the clockwise rotation needed
 * to make that buffer upright (i.e. `ImageProxy.imageInfo.rotationDegrees`).
 *
 * - [OrientationMode.AUTO] reproduces the original behaviour: rotate by [bufferRotation] and
 *   swap width/height when that rotation is 90/270 (so a 1280x720 base streams 720x1280 on a
 *   portrait-held phone, and 1280x720 when the rotation is 0/180).
 * - [OrientationMode.PORTRAIT] forces portrait dims (shorter edge as width). A landscape
 *   buffer must be rotated 90/270 to land there, so the applied rotation is [bufferRotation]
 *   when it is already 90/270, otherwise a default 90.
 * - [OrientationMode.LANDSCAPE] forces landscape dims (longer edge as width). A landscape
 *   buffer needs only 0/180 to keep those dims, so the applied rotation is [bufferRotation]
 *   when it is already 0/180, otherwise 0.
 */
fun effectiveVideo(base: VideoProfile, mode: OrientationMode, bufferRotation: Int): EffectiveVideo {
    val longEdge = maxOf(base.width, base.height)
    val shortEdge = minOf(base.width, base.height)
    return when (mode) {
        OrientationMode.AUTO -> {
            val swap = bufferRotation == 90 || bufferRotation == 270
            val profile =
                if (swap) VideoProfile(base.height, base.width, base.fps) else base
            EffectiveVideo(profile, bufferRotation)
        }
        OrientationMode.PORTRAIT -> {
            val rotation = if (bufferRotation == 270) 270 else 90
            EffectiveVideo(VideoProfile(shortEdge, longEdge, base.fps), rotation)
        }
        OrientationMode.LANDSCAPE -> {
            val rotation = if (bufferRotation == 180) 180 else 0
            EffectiveVideo(VideoProfile(longEdge, shortEdge, base.fps), rotation)
        }
    }
}
