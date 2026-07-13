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
    data class Accepted(val approved: Boolean, val session: String) : PairResult

    /** Server rejected the request or the call failed. */
    data class Failure(val message: String) : PairResult
}

/** Outcome of a GET /status request. */
sealed interface StatusResult {
    data class Ok(val approved: Boolean, val session: String) : StatusResult

    data class Failure(val message: String) : StatusResult
}

/** Control-plane client contract, kept small so tests can supply a fake. */
interface ControlClient {
    fun pair(payload: PairingPayload, phone: PhoneIdentity, rtpPort: Int, ssrc: Long): PairResult

    fun status(payload: PairingPayload): StatusResult
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
        }.toString()

        return try {
            val (code, responseBody) = post("${baseUrl(payload)}/pair", body)
            if (code == HttpURLConnection.HTTP_ACCEPTED) {
                val json = JSONObject(responseBody)
                PairResult.Accepted(
                    approved = json.optBoolean("approved", false),
                    session = json.optString("session", payload.session),
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
