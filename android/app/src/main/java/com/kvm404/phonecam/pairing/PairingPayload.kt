package com.kvm404.phonecam.pairing

import org.json.JSONException
import org.json.JSONObject

/**
 * Video profile advertised in the pairing QR payload.
 */
data class VideoProfile(
    val width: Int,
    val height: Int,
    val fps: Int,
)

/**
 * The pairing payload encoded in the QR shown by `phonecam start`.
 *
 * Mirrors linux-cli/internal/pairing/session.go `Payload`. Parsing is deliberately
 * lenient about unknown extra keys (the client only reads what it needs) but strict
 * about the fields it depends on.
 */
data class PairingPayload(
    val version: Int,
    val name: String,
    val control: String,
    val rtp: String,
    val rtpHost: String,
    val rtpPort: Int,
    val session: String,
    val token: String,
    val expires: String,
    val transport: String,
    val video: VideoProfile,
    val laptopId: String = "",
) {
    companion object {
        const val PROTOCOL_VERSION = 1

        /** Transport advertised by the Linux CLI (session.go sets "rtp-h264"). */
        const val TRANSPORT = "rtp-h264"

        /**
         * Parse the compact JSON QR payload.
         *
         * @throws IllegalArgumentException with a human-readable reason on any invalid input.
         */
        fun parse(text: String): PairingPayload {
            val json = try {
                JSONObject(text)
            } catch (e: JSONException) {
                throw IllegalArgumentException("not a pairing QR (invalid JSON)", e)
            }

            val version = json.optInt("v", -1)
            require(version == PROTOCOL_VERSION) {
                "unsupported protocol version: $version (expected $PROTOCOL_VERSION)"
            }

            val transport = json.optString("transport", "")
            require(transport == TRANSPORT) {
                "unsupported transport: '$transport' (expected '$TRANSPORT')"
            }

            val control = json.optString("control", "")
            require(control.startsWith("http://") || control.startsWith("https://")) {
                "control URL must start with http:// or https://"
            }

            val rtp = json.optString("rtp", "")
            val colon = rtp.lastIndexOf(':')
            require(colon in 1 until rtp.length - 1) {
                "rtp endpoint must be host:port"
            }
            val rtpHost = rtp.substring(0, colon)
            val rtpPort = rtp.substring(colon + 1).toIntOrNull()
            require(rtpHost.isNotBlank()) { "rtp host is blank" }
            require(rtpPort != null && rtpPort in 1..65535) {
                "rtp port is invalid: '${rtp.substring(colon + 1)}'"
            }

            val session = json.optString("session", "")
            require(session.isNotBlank()) { "session id is blank" }

            val token = json.optString("token", "")
            require(token.isNotBlank()) { "token is blank" }

            val expires = json.optString("expires", "")
            require(expires.isNotBlank()) { "expires is blank" }

            val videoJson = json.optJSONObject("video")
                ?: throw IllegalArgumentException("video profile is missing")
            val width = videoJson.optInt("width", 0)
            val height = videoJson.optInt("height", 0)
            val fps = videoJson.optInt("fps", 0)
            require(width > 0) { "video width must be > 0" }
            require(height > 0) { "video height must be > 0" }
            require(fps > 0) { "video fps must be > 0" }

            return PairingPayload(
                version = version,
                name = json.optString("name", ""),
                control = control,
                rtp = rtp,
                rtpHost = rtpHost,
                rtpPort = rtpPort,
                session = session,
                token = token,
                expires = expires,
                transport = transport,
                video = VideoProfile(width, height, fps),
                laptopId = json.optString("laptop_id", ""),
            )
        }
    }
}
