package com.kvm404.phonecam.pairing

import org.json.JSONException
import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.nio.charset.StandardCharsets

/** Phone identity sent to the laptop during pairing. */
data class PhoneIdentity(
    val id: String,
    val name: String,
)

/** Outcome of a POST /pair request. */
sealed interface PairResult {
    /** Server accepted the token (HTTP 202). [approved] is usually false at this point. */
    data class Accepted(
        val approved: Boolean,
        val session: String,
        val resumeToken: String? = null,
        val pairingSecret: String? = null,
    ) : PairResult

    /** Server rejected the request or the call failed. */
    data class Failure(val message: String) : PairResult
}

/** Outcome of a GET /status request. */
sealed interface StatusResult {
    data class Ok(
        val approved: Boolean,
        val session: String,
        val resumeToken: String? = null,
        val pairingSecret: String? = null,
        val lastRtpMs: Long? = null,
        val requestKeyframe: Boolean = false,
    ) : StatusResult

    data class Failure(val message: String) : StatusResult
}

/** Body for POST /reconnect. pairingSecret is sent when held (cross-session trust). */
data class ReconnectRequest(
    val payload: PairingPayload,
    val phone: PhoneIdentity,
    val rtpPort: Int,
    val ssrc: Long,
    val video: VideoProfile,
    val resumeToken: String? = null,
    val pairingSecret: String? = null,
    val camera: String? = null,
)

/** Outcome of a POST /reconnect request. HTTP 200 only. */
sealed interface ReconnectResult {
    data class Ok(
        val approved: Boolean,
        val session: String,
        val resumeToken: String,
        val control: String,
        val rtp: String,
        val video: VideoProfile? = null,
    ) : ReconnectResult

    data class Failure(val message: String) : ReconnectResult
}

/** Control-plane client contract, kept small so tests can supply a fake. */
interface ControlClient {
    fun pair(
        payload: PairingPayload,
        phone: PhoneIdentity,
        rtpPort: Int,
        ssrc: Long,
        video: VideoProfile,
        camera: String? = null,
    ): PairResult

    fun status(payload: PairingPayload): StatusResult

    fun reconnect(request: ReconnectRequest): ReconnectResult
}

/**
 * [ControlClient] backed by [HttpURLConnection] with no third-party HTTP dependency.
 *
 * @param baseUrlOverride when non-null, requests target this base URL instead of the
 *   payload's `control` field. Used by tests to point at a local loopback server.
 */
class HttpControlClient(
    private val baseUrlOverride: String? = null,
    private val connectTimeoutMs: Int = 3000,
    private val readTimeoutMs: Int = 3000,
) : ControlClient {

    private fun baseUrl(payload: PairingPayload): String =
        (baseUrlOverride ?: payload.control).trimEnd('/')

    override fun pair(
        payload: PairingPayload,
        phone: PhoneIdentity,
        rtpPort: Int,
        ssrc: Long,
        video: VideoProfile,
        camera: String?,
    ): PairResult {
        val body = JSONObject().apply {
            put("session", payload.session)
            put("token", payload.token)
            put("phone", JSONObject().apply {
                put("id", phone.id)
                put("name", phone.name)
            })
            put("rtp_port", rtpPort)
            put("ssrc", ssrc)
            // Actual encoder output dims (rotated when the phone streams portrait), so the
            // laptop sizes its receiver to match. fps stays the payload's advertised rate.
            put("video", JSONObject().apply {
                put("width", video.width)
                put("height", video.height)
                put("fps", video.fps)
            })
            camera?.takeIf { it.isNotBlank() }?.let { put("camera", it) }
        }.toString()

        return try {
            val (code, responseBody) = post("${baseUrl(payload)}/pair", body)
            if (code == HttpURLConnection.HTTP_ACCEPTED) {
                val json = JSONObject(responseBody)
                PairResult.Accepted(
                    approved = json.optBoolean("approved", false),
                    session = json.optString("session", payload.session),
                    resumeToken = json.optNonBlank("resume_token"),
                    pairingSecret = json.optNonBlank("pairing_secret"),
                )
            } else {
                PairResult.Failure(errorMessage(code, responseBody))
            }
        } catch (e: JSONException) {
            PairResult.Failure("invalid pairing response from laptop")
        } catch (e: IOException) {
            PairResult.Failure("could not reach laptop: ${e.message ?: "network error"}")
        }
    }

    override fun status(payload: PairingPayload): StatusResult {
        return try {
            val (code, responseBody) = get("${baseUrl(payload)}/status")
            if (code == HttpURLConnection.HTTP_OK) {
                val json = JSONObject(responseBody)
                StatusResult.Ok(
                    approved = json.optBoolean("approved", false),
                    session = json.optString("session", payload.session),
                    resumeToken = json.optNonBlank("resume_token"),
                    pairingSecret = json.optNonBlank("pairing_secret"),
                    lastRtpMs = json.optLongOrNull("last_rtp_ms"),
                    requestKeyframe = json.optBoolean("request_keyframe", false),
                )
            } else {
                StatusResult.Failure(errorMessage(code, responseBody))
            }
        } catch (e: JSONException) {
            StatusResult.Failure("invalid status response from laptop")
        } catch (e: IOException) {
            StatusResult.Failure("could not reach laptop: ${e.message ?: "network error"}")
        }
    }

    override fun reconnect(request: ReconnectRequest): ReconnectResult {
        val body = JSONObject().apply {
            put("phone", JSONObject().apply {
                put("id", request.phone.id)
                put("name", request.phone.name)
            })
            put("rtp_port", request.rtpPort)
            put("ssrc", request.ssrc)
            put("video", JSONObject().apply {
                put("width", request.video.width)
                put("height", request.video.height)
                put("fps", request.video.fps)
            })
            request.resumeToken?.takeIf { it.isNotBlank() }?.let { put("resume_token", it) }
            request.pairingSecret?.takeIf { it.isNotBlank() }?.let { put("pairing_secret", it) }
            request.camera?.takeIf { it.isNotBlank() }?.let { put("camera", it) }
        }.toString()

        return try {
            val (code, responseBody) = post("${baseUrl(request.payload)}/reconnect", body)
            if (code == HttpURLConnection.HTTP_OK) {
                val json = JSONObject(responseBody)
                val videoJson = json.optJSONObject("video")
                ReconnectResult.Ok(
                    approved = json.optBoolean("approved", false),
                    session = json.optString("session", request.payload.session),
                    resumeToken = json.optString("resume_token", ""),
                    control = json.optString("control", ""),
                    rtp = json.optString("rtp", ""),
                    video = videoJson?.let {
                        VideoProfile(
                            it.optInt("width"),
                            it.optInt("height"),
                            it.optInt("fps"),
                        )
                    },
                )
            } else {
                ReconnectResult.Failure(errorMessage(code, responseBody))
            }
        } catch (e: JSONException) {
            ReconnectResult.Failure("invalid reconnect response from laptop")
        } catch (e: IOException) {
            ReconnectResult.Failure("could not reach laptop: ${e.message ?: "network error"}")
        }
    }

    /** Extract the server's "error" field, falling back to the HTTP code. */
    private fun errorMessage(code: Int, body: String): String {
        val fromBody = try {
            JSONObject(body).optString("error", "")
        } catch (e: JSONException) {
            ""
        }
        return if (fromBody.isNotBlank()) fromBody else "laptop returned HTTP $code"
    }

    private fun post(url: String, body: String): Pair<Int, String> {
        val connection = openConnection(url)
        connection.requestMethod = "POST"
        connection.doOutput = true
        connection.setRequestProperty("Content-Type", "application/json")
        connection.setRequestProperty("Accept", "application/json")
        return try {
            connection.outputStream.use { it.write(body.toByteArray(StandardCharsets.UTF_8)) }
            readResponse(connection)
        } finally {
            connection.disconnect()
        }
    }

    private fun get(url: String): Pair<Int, String> {
        val connection = openConnection(url)
        connection.requestMethod = "GET"
        connection.setRequestProperty("Accept", "application/json")
        return try {
            readResponse(connection)
        } finally {
            connection.disconnect()
        }
    }

    private fun openConnection(url: String): HttpURLConnection {
        val connection = URL(url).openConnection() as HttpURLConnection
        connection.connectTimeout = connectTimeoutMs
        connection.readTimeout = readTimeoutMs
        connection.useCaches = false
        return connection
    }

    private fun readResponse(connection: HttpURLConnection): Pair<Int, String> {
        val code = connection.responseCode
        val stream = if (code in 200..299) connection.inputStream else connection.errorStream
        val text = stream?.use { it.readBytes().toString(StandardCharsets.UTF_8) } ?: ""
        return code to text
    }
}

private fun JSONObject.optNonBlank(key: String): String? =
    optString(key).takeIf { it.isNotBlank() }

private fun JSONObject.optLongOrNull(key: String): Long? =
    if (has(key) && !isNull(key)) optLong(key) else null
