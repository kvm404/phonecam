package com.kvm404.phonecam.pairing

import org.json.JSONArray
import org.json.JSONObject

/** One remembered laptop. Secret is the pairing_secret for POST /reconnect. */
data class TrustedLaptop(
    val laptopId: String,
    val name: String,
    val control: String,
    val rtp: String,
    val secret: String,
    val lastSeen: String,
)

/**
 * SharedPreferences "phonecam" / "trusted_laptops" JSON array.
 * Android-free so JVM tests can inject load/save.
 */
class TrustedLaptops(
    private val load: () -> String?,
    private val save: (String) -> Unit,
) {
    fun list(): List<TrustedLaptop> {
        val raw = load().orEmpty()
        if (raw.isBlank()) return emptyList()
        val arr = try {
            JSONArray(raw)
        } catch (_: Exception) {
            return emptyList()
        }
        val out = ArrayList<TrustedLaptop>(arr.length())
        for (i in 0 until arr.length()) {
            val obj = arr.optJSONObject(i) ?: continue
            val id = obj.optString("laptop_id", "")
            val secret = obj.optString("secret", "")
            if (id.isBlank() || secret.isBlank()) continue
            out.add(
                TrustedLaptop(
                    laptopId = id,
                    name = obj.optString("name", ""),
                    control = obj.optString("control", ""),
                    rtp = obj.optString("rtp", ""),
                    secret = secret,
                    lastSeen = obj.optString("last_seen", ""),
                )
            )
        }
        return out.sortedByDescending { it.lastSeen }
    }

    /** Replace the secret if [laptop_id] already exists (fresh QR pair). */
    fun upsert(laptop: TrustedLaptop) {
        if (laptop.laptopId.isBlank() || laptop.secret.isBlank()) return
        val rest = list().filterNot { it.laptopId == laptop.laptopId }
        write(listOf(laptop) + rest)
    }

    fun forget(laptopId: String) {
        write(list().filterNot { it.laptopId == laptopId })
    }

    private fun write(items: List<TrustedLaptop>) {
        val arr = JSONArray()
        for (item in items) {
            arr.put(
                JSONObject().apply {
                    put("laptop_id", item.laptopId)
                    put("name", item.name)
                    put("control", item.control)
                    put("rtp", item.rtp)
                    put("secret", item.secret)
                    put("last_seen", item.lastSeen)
                }
            )
        }
        save(arr.toString())
    }

    companion object {
        const val PREF_FILE = "phonecam"
        const val PREF_KEY = "trusted_laptops"
    }
}
